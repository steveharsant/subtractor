package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A collection joins several uploaded videos (e.g. the parts of a lecture)
// into one combined Whisper transcript. Collections are stored as
// /data/collections/<id>.json; the combined transcript is built on demand
// from each part's cached transcript, so nothing is duplicated on disk.
type CollectionMeta struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	VideoIDs  []string  `json:"videoIds"` // in playback order
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// CollectionPart is one video of a collection as reported to the UI.
type CollectionPart struct {
	ID         string          `json:"id"`
	Filename   string          `json:"filename,omitempty"`
	Size       int64           `json:"size,omitempty"`
	Duration   float64         `json:"duration,omitempty"`
	Transcript *TranscriptInfo `json:"transcript,omitempty"`
	Missing    bool            `json:"missing,omitempty"` // deleted or expired
}

type CollectionView struct {
	CollectionMeta
	Parts    []CollectionPart `json:"parts"`
	Status   string           `json:"status"`   // queued, running, done, error, missing
	Progress int              `json:"progress"` // 0-100, weighted by part duration
	Size     int64            `json:"size"`
	Duration float64          `json:"duration"`
}

const (
	maxCollectionParts = 100
	collectionMissing  = "missing"
)

func (app *App) collectionsDir() string {
	return filepath.Join(app.dataDir, "collections")
}

func (app *App) collectionPath(id string) (string, error) {
	if !videoIDPattern.MatchString(id) {
		return "", errNotFound
	}
	return filepath.Join(app.collectionsDir(), id+".json"), nil
}

func (app *App) loadCollection(id string) (*CollectionMeta, error) {
	path, err := app.collectionPath(id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, errNotFound
	}
	var c CollectionMeta
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (app *App) saveCollection(c *CollectionMeta) error {
	path, err := app.collectionPath(c.ID)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (app *App) listCollections() ([]*CollectionMeta, error) {
	entries, err := os.ReadDir(app.collectionsDir())
	if err != nil {
		return nil, err
	}
	var out []*CollectionMeta
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !videoIDPattern.MatchString(id) {
			continue
		}
		if c, err := app.loadCollection(id); err == nil {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// collectionView resolves a collection's parts and aggregates their status.
func (app *App) collectionView(c *CollectionMeta) *CollectionView {
	v := &CollectionView{CollectionMeta: *c, Parts: make([]CollectionPart, 0, len(c.VideoIDs))}
	var counts = map[string]int{}
	var weightTotal, weightDone float64
	for _, id := range c.VideoIDs {
		part := CollectionPart{ID: id}
		dir, err := app.videoDir(id)
		var meta *VideoMeta
		if err == nil {
			meta, err = loadMeta(dir)
		}
		if err != nil {
			part.Missing = true
			counts[collectionMissing]++
			v.Parts = append(v.Parts, part)
			continue
		}
		part.Filename, part.Size, part.Duration, part.Transcript = meta.Filename, meta.Size, meta.Duration, meta.Transcript
		v.Size += meta.Size
		v.Duration += meta.Duration

		status := transcriptQueued
		progress := 0
		if meta.Transcript != nil {
			status, progress = meta.Transcript.Status, meta.Transcript.Progress
		}
		counts[status]++
		weight := max(meta.Duration, 1)
		weightTotal += weight
		weightDone += weight * float64(progress) / 100
		v.Parts = append(v.Parts, part)
	}

	switch {
	case counts[collectionMissing] > 0:
		v.Status = collectionMissing
	case counts[transcriptError] > 0:
		v.Status = transcriptError
	case counts[transcriptDone] == len(c.VideoIDs):
		v.Status = transcriptDone
	case counts[transcriptRunning] > 0 || counts[transcriptDone] > 0:
		v.Status = transcriptRunning
	default:
		v.Status = transcriptQueued
	}
	if weightTotal > 0 {
		v.Progress = int(weightDone / weightTotal * 100)
	}
	return v
}

func sanitizeCollectionName(name string) string {
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name))
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

func (app *App) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	if !app.whisper.Available() {
		writeError(w, http.StatusServiceUnavailable, "speech-to-text is not available on this server")
		return
	}
	var req struct {
		Name     string   `json:"name"`
		VideoIDs []string `json:"videoIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.VideoIDs) < 2 || len(req.VideoIDs) > maxCollectionParts {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("a combined transcript needs between 2 and %d videos", maxCollectionParts))
		return
	}

	seen := map[string]bool{}
	dirs := make([]string, len(req.VideoIDs))
	var firstName string
	for i, id := range req.VideoIDs {
		if seen[id] {
			writeError(w, http.StatusBadRequest, "a video can only appear once in a combined transcript")
			return
		}
		seen[id] = true
		dir, err := app.videoDir(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, "unknown video "+id)
			return
		}
		meta, err := loadMeta(dir)
		if err != nil {
			writeError(w, http.StatusBadRequest, "unknown video "+id)
			return
		}
		if meta.CollectionID != "" {
			writeError(w, http.StatusConflict, fmt.Sprintf("%q is already part of a combined transcript", meta.Filename))
			return
		}
		if i == 0 {
			firstName = strings.TrimSuffix(meta.Filename, filepath.Ext(meta.Filename))
		}
		dirs[i] = dir
	}

	name := sanitizeCollectionName(req.Name)
	if name == "" {
		name = firstName
	}
	now := time.Now().UTC()
	c := &CollectionMeta{
		ID:        newVideoID(),
		Name:      name,
		VideoIDs:  req.VideoIDs,
		CreatedAt: now,
		ExpiresAt: now.Add(app.retention),
	}
	if err := os.MkdirAll(app.collectionsDir(), 0o750); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to allocate storage")
		return
	}
	if err := app.saveCollection(c); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save combined transcript")
		return
	}
	for _, dir := range dirs {
		app.updateMeta(dir, func(m *VideoMeta) { m.CollectionID = c.ID })
		app.requestTranscript(dir)
	}
	log.Printf("created collection %s (%q, %d parts)", c.ID, c.Name, len(c.VideoIDs))
	writeJSON(w, http.StatusCreated, app.collectionView(c))
}

func (app *App) handleListCollections(w http.ResponseWriter, r *http.Request) {
	cs, err := app.listCollections()
	if err != nil && !os.IsNotExist(err) {
		writeError(w, http.StatusInternalServerError, "failed to list combined transcripts")
		return
	}
	views := make([]*CollectionView, 0, len(cs))
	for _, c := range cs {
		views = append(views, app.collectionView(c))
	}
	writeJSON(w, http.StatusOK, views)
}

func (app *App) handleGetCollection(w http.ResponseWriter, r *http.Request) {
	c, err := app.loadCollection(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "combined transcript not found")
		return
	}
	writeJSON(w, http.StatusOK, app.collectionView(c))
}

// handleDeleteCollection deletes a combined transcript together with its
// part videos, which exist only to feed it.
func (app *App) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	c, err := app.loadCollection(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "combined transcript not found")
		return
	}
	if err := app.deleteCollection(c); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete combined transcript")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (app *App) deleteCollection(c *CollectionMeta) error {
	for _, id := range c.VideoIDs {
		if err := app.deleteVideo(id); err != nil {
			return err
		}
	}
	path, err := app.collectionPath(c.ID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// combinedTranscript joins the parts' transcripts into one SRT timeline and
// one plain-text document with a heading per part.
func (app *App) combinedTranscript(id string) (*CollectionMeta, string, string, int, error) {
	c, err := app.loadCollection(id)
	if err != nil {
		return nil, "", "", http.StatusNotFound, fmt.Errorf("combined transcript not found")
	}
	parts := make([]transcriptPart, 0, len(c.VideoIDs))
	for i, vid := range c.VideoIDs {
		meta, srt, status, err := app.loadTranscript(vid)
		if err != nil {
			if status == http.StatusNotFound {
				return nil, "", "", http.StatusConflict, fmt.Errorf("part %d has been deleted", i+1)
			}
			return nil, "", "", status, fmt.Errorf("part %d: %v", i+1, err)
		}
		parts = append(parts, transcriptPart{Title: meta.Filename, Duration: meta.Duration, SRT: srt})
	}
	srt, txt := combineTranscripts(parts)
	return c, srt, txt, http.StatusOK, nil
}

func (app *App) handleGetCollectionTranscript(w http.ResponseWriter, r *http.Request) {
	_, srt, _, status, err := app.combinedTranscript(r.PathValue("id"))
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": srt})
}

func (app *App) handleDownloadCollection(w http.ResponseWriter, r *http.Request) {
	format, ok := downloadFormat(w, r)
	if !ok {
		return
	}
	c, srt, txt, status, err := app.combinedTranscript(r.PathValue("id"))
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	base := sanitizeFilename(c.Name)
	if format == "txt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", base+".txt"))
		w.Header().Set("Content-Length", strconv.Itoa(len(txt)))
		w.Write([]byte(txt))
		return
	}
	sendSubtitles(w, base, "srt", srt)
}

// ---------- SRT merging ----------

type srtCue struct {
	Start, End time.Duration
	Text       string
}

type transcriptPart struct {
	Title    string
	Duration float64 // seconds; 0 if unknown
	SRT      string
}

var srtTimingLine = regexp.MustCompile(`^(\d{1,2}):(\d{2}):(\d{2})[,.](\d{3})\s+-->\s+(\d{1,2}):(\d{2}):(\d{2})[,.](\d{3})`)

func parseSRTTime(h, m, s, ms string) time.Duration {
	hi, _ := strconv.Atoi(h)
	mi, _ := strconv.Atoi(m)
	si, _ := strconv.Atoi(s)
	msi, _ := strconv.Atoi(ms)
	return time.Duration(hi)*time.Hour + time.Duration(mi)*time.Minute +
		time.Duration(si)*time.Second + time.Duration(msi)*time.Millisecond
}

func formatSRTTime(d time.Duration) string {
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// parseSRT reads the cues of an SRT document; malformed blocks are skipped.
func parseSRT(srt string) []srtCue {
	var cues []srtCue
	for _, block := range strings.Split(strings.ReplaceAll(srt, "\r\n", "\n"), "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		for i, line := range lines {
			m := srtTimingLine.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			cues = append(cues, srtCue{
				Start: parseSRTTime(m[1], m[2], m[3], m[4]),
				End:   parseSRTTime(m[5], m[6], m[7], m[8]),
				Text:  strings.TrimSpace(strings.Join(lines[i+1:], "\n")),
			})
			break
		}
	}
	return cues
}

// combineTranscripts shifts each part's cues by the running length of the
// parts before it, giving one continuous, renumbered SRT. A part with an
// unknown duration advances the timeline to its last cue instead.
func combineTranscripts(parts []transcriptPart) (string, string) {
	var srt, txt strings.Builder
	var offset time.Duration
	n := 0
	for i, p := range parts {
		cues := parseSRT(p.SRT)
		partLen := time.Duration(p.Duration * float64(time.Second))
		for _, c := range cues {
			// Whisper can run a cue slightly past the end of the audio;
			// clamp it so it cannot overlap the next part.
			if partLen > 0 {
				c.Start, c.End = min(c.Start, partLen), min(c.End, partLen)
			}
			n++
			fmt.Fprintf(&srt, "%d\n%s --> %s\n%s\n\n", n, formatSRTTime(c.Start+offset), formatSRTTime(c.End+offset), c.Text)
		}

		if i > 0 {
			txt.WriteString("\n")
		}
		fmt.Fprintf(&txt, "=== Part %d: %s ===\n\n", i+1, p.Title)
		if body := strings.TrimSpace(srtToPlainText(p.SRT)); body != "" {
			txt.WriteString(body)
			txt.WriteString("\n")
		}

		if partLen > 0 {
			offset += partLen
		} else if len(cues) > 0 {
			offset += cues[len(cues)-1].End
		}
	}
	return srt.String(), txt.String()
}
