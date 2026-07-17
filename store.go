package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// SubtitleStream describes one embedded subtitle stream in a video.
type SubtitleStream struct {
	Index       int    `json:"index"` // absolute stream index within the container
	Codec       string `json:"codec"`
	Language    string `json:"language,omitempty"`
	Title       string `json:"title,omitempty"`
	Default     bool   `json:"default"`
	Forced      bool   `json:"forced"`
	Extractable bool   `json:"extractable"` // false for bitmap formats (PGS, VobSub, ...)
}

// VideoMeta is persisted as meta.json alongside each uploaded video.
type VideoMeta struct {
	ID         string           `json:"id"`
	Filename   string           `json:"filename"`
	Size       int64            `json:"size"`
	Duration   float64          `json:"duration,omitempty"` // seconds
	Format     string           `json:"format,omitempty"`
	UploadedAt time.Time        `json:"uploadedAt"`
	ExpiresAt  time.Time        `json:"expiresAt"`
	Streams    []SubtitleStream `json:"streams"`
	StoredName string           `json:"storedName"` // filename of the video inside its directory
}

var videoIDPattern = regexp.MustCompile(`^[a-f0-9]{24}$`)

func newVideoID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand failure: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

var errNotFound = errors.New("not found")

// videoDir returns the storage directory for a video ID, rejecting malformed IDs
// so path traversal via the URL is impossible.
func (app *App) videoDir(id string) (string, error) {
	if !videoIDPattern.MatchString(id) {
		return "", errNotFound
	}
	dir := filepath.Join(app.videosDir(), id)
	if _, err := os.Stat(dir); err != nil {
		return "", errNotFound
	}
	return dir, nil
}

func metaPath(dir string) string {
	return filepath.Join(dir, "meta.json")
}

func loadMeta(dir string) (*VideoMeta, error) {
	b, err := os.ReadFile(metaPath(dir))
	if err != nil {
		return nil, err
	}
	var meta VideoMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func saveMeta(dir string, meta *VideoMeta) error {
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := metaPath(dir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, metaPath(dir))
}

// listVideos returns metadata for all stored videos, newest first.
func (app *App) listVideos() ([]*VideoMeta, error) {
	entries, err := os.ReadDir(app.videosDir())
	if err != nil {
		return nil, err
	}
	videos := make([]*VideoMeta, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !videoIDPattern.MatchString(e.Name()) {
			continue
		}
		meta, err := loadMeta(filepath.Join(app.videosDir(), e.Name()))
		if err != nil {
			continue // skip incomplete/corrupt entries; the sweeper will reap them
		}
		videos = append(videos, meta)
	}
	sort.Slice(videos, func(i, j int) bool {
		return videos[i].UploadedAt.After(videos[j].UploadedAt)
	})
	return videos, nil
}
