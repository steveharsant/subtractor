package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// whisperSRTName is the cached Whisper transcript inside a video's subs dir.
const whisperSRTName = "whisper.srt"

// Transcriber runs Whisper jobs one at a time: whisper.cpp already uses every
// core it is given, so running jobs in parallel would only slow each down.
type Transcriber struct {
	app *App

	mu      sync.Mutex
	queue   []string // video IDs, FIFO
	current string
	cancel  context.CancelFunc
	done    chan struct{} // closed when the current job finishes
	wake    chan struct{}
}

func NewTranscriber(app *App) *Transcriber {
	return &Transcriber{app: app, wake: make(chan struct{}, 1)}
}

// Enqueue adds a video to the queue unless it is already queued or running.
func (t *Transcriber) Enqueue(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == id {
		return
	}
	for _, q := range t.queue {
		if q == id {
			return
		}
	}
	t.queue = append(t.queue, id)
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// Cancel removes a video from the queue and, if it is being transcribed,
// stops the job and waits for it to exit so its files can be removed safely.
func (t *Transcriber) Cancel(id string) {
	t.mu.Lock()
	for i, q := range t.queue {
		if q == id {
			t.queue = append(t.queue[:i], t.queue[i+1:]...)
			break
		}
	}
	var wait chan struct{}
	if t.current == id && t.cancel != nil {
		t.cancel()
		wait = t.done
	}
	t.mu.Unlock()
	if wait != nil {
		<-wait
	}
}

// Resume re-queues work interrupted by a restart, oldest first.
func (t *Transcriber) Resume() {
	videos, err := t.app.listVideos()
	if err != nil {
		return
	}
	sort.Slice(videos, func(i, j int) bool {
		return videos[i].Transcript.pendingSince().Before(videos[j].Transcript.pendingSince())
	})
	for _, v := range videos {
		if v.Transcript.pending() {
			t.Enqueue(v.ID)
		}
	}
}

func (ti *TranscriptInfo) pendingSince() time.Time {
	if ti == nil {
		return time.Time{}
	}
	return ti.UpdatedAt
}

// Run processes the queue forever. Jobs wait until the model is available.
func (t *Transcriber) Run() {
	<-t.app.whisper.Ready()
	for {
		t.mu.Lock()
		if len(t.queue) == 0 {
			t.mu.Unlock()
			<-t.wake
			continue
		}
		id := t.queue[0]
		t.queue = t.queue[1:]
		ctx, cancel := context.WithCancel(context.Background())
		t.current, t.cancel, t.done = id, cancel, make(chan struct{})
		t.mu.Unlock()

		t.process(ctx, id)

		t.mu.Lock()
		cancel()
		close(t.done)
		t.current, t.cancel, t.done = "", nil, nil
		t.mu.Unlock()
	}
}

func (t *Transcriber) process(ctx context.Context, id string) {
	app := t.app
	dir, err := app.videoDir(id)
	if err != nil {
		return // deleted while queued
	}
	meta, err := app.updateMeta(dir, func(m *VideoMeta) {
		m.Transcript = &TranscriptInfo{Status: transcriptRunning, UpdatedAt: time.Now().UTC()}
	})
	if err != nil {
		return
	}

	// Allow generous time: large models on slow CPUs can run slower than
	// real time.
	timeout := 30*time.Minute + time.Duration(meta.Duration*6)*time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	lastPct := -1
	language, err := transcribeVideo(ctx, app.whisper, dir, meta.StoredName, func(pct int) {
		if pct == lastPct {
			return
		}
		lastPct = pct
		app.updateMeta(dir, func(m *VideoMeta) {
			if m.Transcript != nil {
				m.Transcript.Progress = pct
				m.Transcript.UpdatedAt = time.Now().UTC()
			}
		})
	})

	if errors.Is(err, context.Canceled) {
		return // video deleted mid-job; nothing left to update
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("transcription timed out after %s", timeout)
	}
	app.updateMeta(dir, func(m *VideoMeta) {
		ti := &TranscriptInfo{Model: app.whisper.Model.Name, UpdatedAt: time.Now().UTC()}
		if err != nil {
			ti.Status, ti.Error = transcriptError, err.Error()
		} else {
			ti.Status, ti.Progress, ti.Language = transcriptDone, 100, language
		}
		m.Transcript = ti
	})
	if err != nil {
		log.Printf("transcription of %s failed: %v", id, err)
	} else {
		log.Printf("transcribed %s (%s) in %s", id, meta.Filename, time.Since(started).Round(time.Second))
	}
}

// transcribeVideo extracts the audio track as 16 kHz mono WAV (what whisper
// expects) and runs whisper on it, leaving subs/whisper.srt.
func transcribeVideo(ctx context.Context, w *Whisper, videoDir, storedName string, onProgress func(int)) (string, error) {
	subsDir := filepath.Join(videoDir, "subs")
	if err := os.MkdirAll(subsDir, 0o750); err != nil {
		return "", err
	}
	wav := filepath.Join(videoDir, "audio.wav")
	defer os.Remove(wav)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin", "-v", "error", "-y",
		"-i", filepath.Join(videoDir, storedName),
		"-map", "0:a:0",
		"-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le",
		wav,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "matches no streams") {
			return "", fmt.Errorf("the video has no audio track to transcribe")
		}
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return "", fmt.Errorf("audio extraction failed: %s", msg)
	}

	outBase := filepath.Join(subsDir, "whisper.tmp")
	defer os.Remove(outBase + ".srt")
	language, err := w.transcribe(ctx, wav, outBase, onProgress)
	if err != nil {
		return "", err
	}
	if err := os.Rename(outBase+".srt", filepath.Join(subsDir, whisperSRTName)); err != nil {
		return "", fmt.Errorf("whisper produced no transcript: %w", err)
	}
	return language, nil
}

// requestTranscript queues a video for transcription unless it is already
// queued, running or done. Callers hold no locks.
func (app *App) requestTranscript(dir string) (*VideoMeta, error) {
	queue := false
	meta, err := app.updateMeta(dir, func(m *VideoMeta) {
		if m.Transcript.pending() || (m.Transcript != nil && m.Transcript.Status == transcriptDone) {
			return
		}
		m.Transcript = &TranscriptInfo{Status: transcriptQueued, UpdatedAt: time.Now().UTC()}
		queue = true
	})
	if err != nil {
		return nil, err
	}
	if queue {
		app.transcriber.Enqueue(meta.ID)
	}
	return meta, nil
}

func (app *App) handleWhisperStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, app.whisper.Status())
}

func (app *App) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	if !app.whisper.Available() {
		writeError(w, http.StatusServiceUnavailable, "speech-to-text is not available on this server")
		return
	}
	dir, err := app.videoDir(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	meta, err := app.requestTranscript(dir)
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	writeJSON(w, http.StatusAccepted, meta)
}

// loadTranscript returns a video's finished Whisper SRT.
func (app *App) loadTranscript(id string) (*VideoMeta, string, int, error) {
	dir, err := app.videoDir(id)
	if err != nil {
		return nil, "", http.StatusNotFound, fmt.Errorf("video not found")
	}
	meta, err := loadMeta(dir)
	if err != nil {
		return nil, "", http.StatusNotFound, fmt.Errorf("video not found")
	}
	if meta.Transcript == nil || meta.Transcript.Status != transcriptDone {
		return nil, "", http.StatusConflict, fmt.Errorf("transcript is not ready")
	}
	b, err := os.ReadFile(filepath.Join(dir, "subs", whisperSRTName))
	if err != nil {
		return nil, "", http.StatusInternalServerError, fmt.Errorf("failed to read transcript")
	}
	return meta, string(b), http.StatusOK, nil
}

func (app *App) handleGetTranscript(w http.ResponseWriter, r *http.Request) {
	_, content, status, err := app.loadTranscript(r.PathValue("id"))
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}

func (app *App) handleDownloadTranscript(w http.ResponseWriter, r *http.Request) {
	format, ok := downloadFormat(w, r)
	if !ok {
		return
	}
	meta, content, status, err := app.loadTranscript(r.PathValue("id"))
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	base := strings.TrimSuffix(meta.Filename, filepath.Ext(meta.Filename))
	sendSubtitles(w, base+".transcript", format, content)
}
