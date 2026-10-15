package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("failed to encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// sanitizeFilename reduces an uploaded filename to a safe display name.
func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if len(name) > 200 {
		name = name[len(name)-200:]
	}
	if name == "" || name == "." || name == ".." {
		name = "upload"
	}
	return name
}

func (app *App) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, app.maxUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data upload")
		return
	}

	id := newVideoID()
	dir := filepath.Join(app.videosDir(), id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to allocate storage")
		return
	}
	cleanup := func() { os.RemoveAll(dir) }

	var meta *VideoMeta
	transcribe := false
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			cleanup()
			if maxErr := new(http.MaxBytesError); errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds the %d MB upload limit", app.maxUploadBytes>>20))
				return
			}
			writeError(w, http.StatusBadRequest, "malformed upload")
			return
		}
		if part.FormName() == "transcribe" {
			// Must precede the file field in the form.
			v, _ := io.ReadAll(io.LimitReader(part, 8))
			transcribe = string(v) == "1" || string(v) == "true"
			continue
		}
		if part.FormName() != "file" {
			continue
		}

		filename := sanitizeFilename(part.FileName())
		ext := strings.ToLower(filepath.Ext(filename))
		if ext == "" {
			ext = ".bin"
		}
		storedName := "video" + ext
		dst, err := os.OpenFile(filepath.Join(dir, storedName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
		if err != nil {
			cleanup()
			writeError(w, http.StatusInternalServerError, "failed to store upload")
			return
		}
		size, err := io.Copy(dst, part)
		dst.Close()
		if err != nil {
			cleanup()
			if maxErr := new(http.MaxBytesError); errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds the %d MB upload limit", app.maxUploadBytes>>20))
				return
			}
			writeError(w, http.StatusBadRequest, "upload interrupted")
			return
		}

		streams, format, duration, err := probeVideo(filepath.Join(dir, storedName))
		if err != nil {
			cleanup()
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}

		now := time.Now().UTC()
		meta = &VideoMeta{
			ID:         id,
			Filename:   filename,
			Size:       size,
			Duration:   duration,
			Format:     format,
			UploadedAt: now,
			ExpiresAt:  now.Add(app.retention),
			Streams:    streams,
			StoredName: storedName,
		}
		if err := saveMeta(dir, meta); err != nil {
			cleanup()
			writeError(w, http.StatusInternalServerError, "failed to save metadata")
			return
		}
		break // single file per upload
	}

	if meta == nil {
		cleanup()
		writeError(w, http.StatusBadRequest, "no file field in upload")
		return
	}
	log.Printf("uploaded %s (%s, %d bytes, %d subtitle streams)", meta.ID, meta.Filename, meta.Size, len(meta.Streams))
	if transcribe && app.whisper.Available() {
		if queued, err := app.requestTranscript(dir); err == nil {
			meta = queued
		}
	}
	writeJSON(w, http.StatusCreated, meta)
}

func (app *App) handleListVideos(w http.ResponseWriter, r *http.Request) {
	videos, err := app.listVideos()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list videos")
		return
	}
	writeJSON(w, http.StatusOK, videos)
}

func (app *App) handleGetVideo(w http.ResponseWriter, r *http.Request) {
	dir, err := app.videoDir(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	meta, err := loadMeta(dir)
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func (app *App) handleDeleteVideo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := app.videoDir(id); err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	if err := app.deleteVideo(id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete video")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// deleteVideo stops any transcription of the video, then removes its files.
func (app *App) deleteVideo(id string) error {
	dir, err := app.videoDir(id)
	if err != nil {
		return nil // already gone
	}
	app.transcriber.Cancel(id)
	return os.RemoveAll(dir)
}

// resolveStream validates that the requested stream index is a known,
// extractable subtitle stream of the video.
func resolveStream(meta *VideoMeta, streamStr string) (*SubtitleStream, error) {
	idx, err := strconv.Atoi(streamStr)
	if err != nil {
		return nil, fmt.Errorf("invalid stream index")
	}
	for i := range meta.Streams {
		if meta.Streams[i].Index == idx {
			if !meta.Streams[i].Extractable {
				return nil, fmt.Errorf("stream is a bitmap subtitle format (%s) and cannot be converted to text", meta.Streams[i].Codec)
			}
			return &meta.Streams[i], nil
		}
	}
	return nil, fmt.Errorf("no such subtitle stream")
}

func (app *App) handleExtract(w http.ResponseWriter, r *http.Request) {
	dir, err := app.videoDir(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	meta, err := loadMeta(dir)
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	stream, err := resolveStream(meta, r.PathValue("stream"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	srtPath, err := extractSubtitle(dir, meta.StoredName, stream.Index)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	content, err := os.ReadFile(srtPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read extracted subtitles")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stream":  stream,
		"content": string(content),
	})
}

func (app *App) handleDownload(w http.ResponseWriter, r *http.Request) {
	format, ok := downloadFormat(w, r)
	if !ok {
		return
	}
	dir, err := app.videoDir(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	meta, err := loadMeta(dir)
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	stream, err := resolveStream(meta, r.PathValue("stream"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	srtPath, err := extractSubtitle(dir, meta.StoredName, stream.Index)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	content, err := os.ReadFile(srtPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read extracted subtitles")
		return
	}

	base := strings.TrimSuffix(meta.Filename, filepath.Ext(meta.Filename))
	label := stream.Language
	if label == "" {
		label = fmt.Sprintf("stream%d", stream.Index)
	}
	sendSubtitles(w, base+"."+label, format, string(content))
}

// downloadFormat validates the ?format= query parameter of a download.
func downloadFormat(w http.ResponseWriter, r *http.Request) (string, bool) {
	format := r.URL.Query().Get("format")
	if format != "srt" && format != "txt" {
		writeError(w, http.StatusBadRequest, "format must be srt or txt")
		return "", false
	}
	return format, true
}

// sendSubtitles writes SRT content as an attachment, converting it to plain
// text first when format is "txt".
func sendSubtitles(w http.ResponseWriter, baseName, format, srt string) {
	content := srt
	if format == "txt" {
		content = srtToPlainText(srt)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", baseName+"."+format))
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.Write([]byte(content))
}
