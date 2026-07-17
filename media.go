package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	probeTimeout   = 2 * time.Minute
	extractTimeout = 15 * time.Minute
)

// Text-based subtitle codecs that ffmpeg can convert to SRT. Bitmap formats
// (hdmv_pgs_subtitle, dvd_subtitle, dvb_subtitle, xsub) would need OCR, which
// is out of scope, so they are listed but marked non-extractable.
var textSubtitleCodecs = map[string]bool{
	"subrip":   true,
	"srt":      true,
	"ass":      true,
	"ssa":      true,
	"webvtt":   true,
	"mov_text": true,
	"text":     true,
	"eia_608":  true,
}

type ffprobeOutput struct {
	Streams []struct {
		Index       int    `json:"index"`
		CodecName   string `json:"codec_name"`
		CodecType   string `json:"codec_type"`
		Disposition struct {
			Default int `json:"default"`
			Forced  int `json:"forced"`
		} `json:"disposition"`
		Tags struct {
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"tags"`
	} `json:"streams"`
	Format struct {
		FormatLongName string `json:"format_long_name"`
		Duration       string `json:"duration"`
	} `json:"format"`
}

// probeVideo runs ffprobe on the file and returns its subtitle streams plus
// container details. An error means the file is not a readable video.
func probeVideo(path string) ([]SubtitleStream, string, float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, "", 0, fmt.Errorf("ffprobe failed: file does not appear to be a valid video")
	}

	var probe ffprobeOutput
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, "", 0, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	hasMedia := false
	var subs []SubtitleStream
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "video", "audio":
			hasMedia = true
		case "subtitle":
			subs = append(subs, SubtitleStream{
				Index:       s.Index,
				Codec:       s.CodecName,
				Language:    s.Tags.Language,
				Title:       s.Tags.Title,
				Default:     s.Disposition.Default == 1,
				Forced:      s.Disposition.Forced == 1,
				Extractable: textSubtitleCodecs[s.CodecName],
			})
		}
	}
	if !hasMedia {
		return nil, "", 0, fmt.Errorf("file contains no video or audio streams")
	}

	duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	return subs, probe.Format.FormatLongName, duration, nil
}

// extractSubtitle demuxes one subtitle stream to SRT, caching the result in
// the video's directory. Returns the path to the cached .srt file.
func extractSubtitle(videoDir, storedName string, streamIndex int) (string, error) {
	subsDir := filepath.Join(videoDir, "subs")
	if err := os.MkdirAll(subsDir, 0o750); err != nil {
		return "", err
	}
	outPath := filepath.Join(subsDir, fmt.Sprintf("%d.srt", streamIndex))
	if _, err := os.Stat(outPath); err == nil {
		return outPath, nil // already extracted
	}

	ctx, cancel := context.WithTimeout(context.Background(), extractTimeout)
	defer cancel()

	tmpPath := outPath + ".tmp.srt"
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error",
		"-y",
		"-i", filepath.Join(videoDir, storedName),
		"-map", fmt.Sprintf("0:%d", streamIndex),
		"-c:s", "srt",
		tmpPath,
	)
	stderr, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmpPath)
		msg := strings.TrimSpace(string(stderr))
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return "", fmt.Errorf("ffmpeg extraction failed: %s", msg)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		return "", err
	}
	return outPath, nil
}

var (
	srtTimestampLine = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}[,.]\d{3}\s+-->\s+\d{2}:\d{2}:\d{2}[,.]\d{3}`)
	srtIndexLine     = regexp.MustCompile(`^\d+$`)
	markupTags       = regexp.MustCompile(`<[^>]*>|\{\\[^}]*\}`)
)

// srtToPlainText strips sequence numbers, timestamps and markup from SRT
// content, leaving one line per cue line of dialogue. Cues are parsed as
// blank-line-separated blocks so dialogue that is itself a bare number is
// not mistaken for a cue index.
func srtToPlainText(srt string) string {
	var b strings.Builder
	blocks := strings.Split(strings.ReplaceAll(srt, "\r\n", "\n"), "\n\n")
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 {
			continue
		}
		// Drop the leading cue index and timestamp lines of the block.
		if srtIndexLine.MatchString(strings.TrimSpace(lines[0])) {
			lines = lines[1:]
		}
		if len(lines) > 0 && srtTimestampLine.MatchString(strings.TrimSpace(lines[0])) {
			lines = lines[1:]
		}
		wrote := false
		for _, line := range lines {
			text := strings.TrimSpace(markupTags.ReplaceAllString(line, ""))
			if text == "" {
				continue
			}
			b.WriteString(text)
			b.WriteString("\n")
			wrote = true
		}
		if wrote {
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String()) + "\n"
}
