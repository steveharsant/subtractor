package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	regexpLanguage         = regexp.MustCompile(`^(auto|[a-z]{2,3})$`)
	regexpProgress         = regexp.MustCompile(`progress\s*=\s*(\d+)%`)
	regexpDetectedLanguage = regexp.MustCompile(`auto-detected language:\s*([a-z]{2,3})\b`)
)

// whisperModel is one ggml Whisper model published in the whisper.cpp model
// repository. Sizes and hashes are for the pinned repository revision below,
// so a tampered or truncated download is rejected.
type whisperModel struct {
	Name   string
	File   string
	Size   int64
	SHA256 string
	// Auto-selection requirements. A zero MinMem means the model is never
	// picked automatically (it can still be chosen explicitly).
	MinMem    int64
	MinCPUs   int
	NeedsAVX2 bool
}

const (
	defaultModelBaseURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1"
	gib                 = int64(1) << 30
)

// whisperModels is ordered best-first; auto-selection takes the first model
// whose requirements the environment meets.
var whisperModels = []whisperModel{
	{Name: "large-v3", File: "ggml-large-v3.bin", Size: 3095033483, SHA256: "64d182b440b98d5203c4f9bd541544d84c605196c4f7b845dfa11fb23594d1e2"},
	{Name: "large-v3-turbo", File: "ggml-large-v3-turbo.bin", Size: 1624555275, SHA256: "1fc70f774d38eb169993ac391eea357ef47c88757ef72ee5943879b7e8e2bc69", MinMem: 6 * gib, MinCPUs: 8, NeedsAVX2: true},
	{Name: "medium", File: "ggml-medium.bin", Size: 1533763059, SHA256: "6c14d5adee5f86394037b4e4e8b59f1673b6cee10e3cf0b11bbdbee79c156208"},
	{Name: "small", File: "ggml-small.bin", Size: 487601967, SHA256: "1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b", MinMem: 3 * gib, MinCPUs: 4},
	{Name: "base", File: "ggml-base.bin", Size: 147951465, SHA256: "60ed5bc3dd14eea856493d334349b405782ddcaf0028d4b5df4088345fba2efe", MinMem: 3 * gib / 2, MinCPUs: 2},
	{Name: "tiny", File: "ggml-tiny.bin", Size: 77691713, SHA256: "be07e048e1e599ad46341c8d2a135645097a538221678b7acdd1b1919c6e1b21", MinMem: 1, MinCPUs: 1},
}

func findWhisperModel(name string) (whisperModel, bool) {
	for _, m := range whisperModels {
		if m.Name == name {
			return m, true
		}
	}
	return whisperModel{}, false
}

// hostResources is what the container can actually use, after cgroup limits.
type hostResources struct {
	CPUs     int   `json:"cpus"`
	MemBytes int64 `json:"memBytes"`
	AVX2     bool  `json:"avx2"` // or an equivalent fast SIMD path on non-x86
}

func detectResources() hostResources {
	res := hostResources{CPUs: runtime.NumCPU(), AVX2: true}

	// CPU quota: cgroup v2, then v1.
	if b, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		if cpus, ok := parseCgroupV2CPUMax(string(b)); ok && cpus < res.CPUs {
			res.CPUs = cpus
		}
	} else {
		quota, qerr := readInt("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
		period, perr := readInt("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
		if qerr == nil && perr == nil && quota > 0 && period > 0 {
			if cpus := int(math.Ceil(float64(quota) / float64(period))); cpus < res.CPUs {
				res.CPUs = cpus
			}
		}
	}

	// Memory: total RAM (stable across restarts, unlike free memory, so the
	// chosen model does not flip-flop), capped by any cgroup limit.
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		res.MemBytes = parseMemTotal(string(b))
	}
	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		if b, err := os.ReadFile(path); err == nil {
			if limit, ok := parseCgroupMemLimit(string(b)); ok && (res.MemBytes == 0 || limit < res.MemBytes) {
				res.MemBytes = limit
			}
			break
		}
	}

	if runtime.GOARCH == "amd64" {
		if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			res.AVX2 = cpuinfoHasFlag(string(b), "avx2")
		}
	}
	return res
}

func readInt(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
}

// parseCgroupV2CPUMax parses cpu.max ("max 100000" or "200000 100000").
func parseCgroupV2CPUMax(s string) (int, bool) {
	fields := strings.Fields(s)
	if len(fields) != 2 || fields[0] == "max" {
		return 0, false
	}
	quota, err1 := strconv.ParseFloat(fields[0], 64)
	period, err2 := strconv.ParseFloat(fields[1], 64)
	if err1 != nil || err2 != nil || quota <= 0 || period <= 0 {
		return 0, false
	}
	return max(1, int(math.Ceil(quota/period))), true
}

// parseCgroupMemLimit parses memory.max / memory.limit_in_bytes. cgroup v1
// reports "unlimited" as a huge number, which is treated as no limit.
func parseCgroupMemLimit(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "max" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || n >= 1<<60 {
		return 0, false
	}
	return n, true
}

func parseMemTotal(meminfo string) int64 {
	for _, line := range strings.Split(meminfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil {
				return kb * 1024
			}
		}
	}
	return 0
}

func cpuinfoHasFlag(cpuinfo, flag string) bool {
	for _, line := range strings.Split(cpuinfo, "\n") {
		if strings.HasPrefix(line, "flags") {
			for _, f := range strings.Fields(line) {
				if f == flag {
					return true
				}
			}
			return false
		}
	}
	return false
}

// selectWhisperModel picks the best model the environment can run at a
// reasonable speed, and explains the choice.
func selectWhisperModel(res hostResources) (whisperModel, string) {
	for _, m := range whisperModels {
		if m.MinMem == 0 {
			continue
		}
		if res.MemBytes >= m.MinMem && res.CPUs >= m.MinCPUs && (!m.NeedsAVX2 || res.AVX2) {
			reason := fmt.Sprintf("auto-selected for %d CPUs, %.1f GiB RAM", res.CPUs, float64(res.MemBytes)/float64(gib))
			if !res.AVX2 {
				reason += ", no AVX2"
			}
			return m, reason
		}
	}
	m, _ := findWhisperModel("tiny")
	return m, "auto-selected: resources could not be determined"
}

func defaultWhisperThreads(cpus int) int {
	// whisper.cpp stops scaling well beyond ~16 threads.
	return min(max(cpus, 1), 16)
}

// Whisper states reported to the UI.
const (
	whisperUnavailable = "unavailable" // whisper-cli is not installed (e.g. local development)
	whisperDownloading = "downloading"
	whisperReady       = "ready"
	whisperFailed      = "error"
)

// Whisper owns the whisper-cli binary, the chosen model and its first-run
// download.
type Whisper struct {
	Bin       string
	ModelPath string
	Model     whisperModel
	Reason    string
	Language  string
	Threads   int
	Resources hostResources

	baseURL string
	ready   chan struct{} // closed once the model is on disk and verified

	mu         sync.Mutex
	state      string
	downloaded int64
	err        string
}

type WhisperStatus struct {
	State      string        `json:"state"`
	Model      string        `json:"model"`
	Reason     string        `json:"reason"`
	Language   string        `json:"language"`
	Threads    int           `json:"threads"`
	Resources  hostResources `json:"resources"`
	Downloaded int64         `json:"downloaded,omitempty"`
	Total      int64         `json:"total,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// NewWhisper reads configuration from the environment and evaluates the
// host. It does not touch the network; call Start for that.
func NewWhisper(dataDir string) (*Whisper, error) {
	w := &Whisper{
		Bin:       envOr("SUBTRACTOR_WHISPER_BIN", "whisper-cli"),
		Language:  envOr("SUBTRACTOR_WHISPER_LANGUAGE", "auto"),
		Resources: detectResources(),
		baseURL:   strings.TrimRight(envOr("SUBTRACTOR_WHISPER_MODEL_BASE_URL", defaultModelBaseURL), "/"),
		ready:     make(chan struct{}),
	}
	if !regexpLanguage.MatchString(w.Language) {
		return nil, fmt.Errorf("SUBTRACTOR_WHISPER_LANGUAGE must be 'auto' or a language code like 'en'")
	}

	switch name := envOr("SUBTRACTOR_WHISPER_MODEL", "auto"); name {
	case "auto":
		w.Model, w.Reason = selectWhisperModel(w.Resources)
	default:
		m, ok := findWhisperModel(name)
		if !ok {
			names := make([]string, len(whisperModels))
			for i, m := range whisperModels {
				names[i] = m.Name
			}
			return nil, fmt.Errorf("SUBTRACTOR_WHISPER_MODEL must be 'auto' or one of: %s", strings.Join(names, ", "))
		}
		w.Model, w.Reason = m, "set by SUBTRACTOR_WHISPER_MODEL"
	}

	w.Threads = defaultWhisperThreads(w.Resources.CPUs)
	if v := os.Getenv("SUBTRACTOR_WHISPER_THREADS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("SUBTRACTOR_WHISPER_THREADS must be a positive integer")
		}
		w.Threads = n
	}

	w.ModelPath = filepath.Join(dataDir, "models", w.Model.File)
	return w, nil
}

// Start resolves the whisper binary and, if needed, downloads the model in
// the background. The app keeps serving meanwhile; transcription jobs wait
// on Ready().
func (w *Whisper) Start() {
	bin, err := exec.LookPath(w.Bin)
	if err != nil {
		w.setState(whisperUnavailable, fmt.Sprintf("%s not found; speech-to-text is disabled", w.Bin))
		log.Printf("whisper: %s not found on PATH; speech-to-text disabled", w.Bin)
		return
	}
	w.Bin = bin
	log.Printf("whisper: model %s (%s), %d threads, language %s", w.Model.Name, w.Reason, w.Threads, w.Language)

	if info, err := os.Stat(w.ModelPath); err == nil && info.Size() == w.Model.Size {
		w.markReady()
		return
	}
	w.setState(whisperDownloading, "")
	go w.downloadLoop()
}

func (w *Whisper) Ready() <-chan struct{} { return w.ready }

// Available reports whether transcription can ever run in this process.
func (w *Whisper) Available() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state != whisperUnavailable
}

func (w *Whisper) Status() WhisperStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := WhisperStatus{
		State:     w.state,
		Model:     w.Model.Name,
		Reason:    w.Reason,
		Language:  w.Language,
		Threads:   w.Threads,
		Resources: w.Resources,
		Error:     w.err,
	}
	if w.state == whisperDownloading {
		st.Downloaded, st.Total = w.downloaded, w.Model.Size
	}
	return st
}

func (w *Whisper) setState(state, errMsg string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state, w.err = state, errMsg
}

func (w *Whisper) markReady() {
	w.setState(whisperReady, "")
	close(w.ready)
	log.Printf("whisper: model %s ready at %s", w.Model.Name, w.ModelPath)
}

// downloadLoop retries the first-run download with backoff until it succeeds.
func (w *Whisper) downloadLoop() {
	backoff := 30 * time.Second
	for {
		err := w.download()
		if err == nil {
			w.markReady()
			return
		}
		log.Printf("whisper: model download failed: %v (retrying in %s)", err, backoff)
		w.setState(whisperFailed, fmt.Sprintf("model download failed: %v (retrying in %s)", err, backoff))
		time.Sleep(backoff)
		backoff = min(backoff*2, 30*time.Minute)
		w.setState(whisperDownloading, "")
	}
}

func (w *Whisper) download() error {
	if err := os.MkdirAll(filepath.Dir(w.ModelPath), 0o750); err != nil {
		return err
	}
	url := w.baseURL + "/" + w.Model.File
	log.Printf("whisper: downloading %s (%d MB) from %s", w.Model.File, w.Model.Size>>20, url)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	tmp := w.ModelPath + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	defer os.Remove(tmp) // no-op after a successful rename

	hash := sha256.New()
	counter := &progressWriter{onWrite: func(n int64) {
		w.mu.Lock()
		w.downloaded = n
		w.mu.Unlock()
	}}
	// Read at most one byte past the expected size so an oversized response
	// fails the size check instead of filling the disk.
	_, err = io.Copy(io.MultiWriter(f, hash, counter), io.LimitReader(resp.Body, w.Model.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if counter.n != w.Model.Size {
		return fmt.Errorf("size mismatch: got %d bytes, expected %d", counter.n, w.Model.Size)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != w.Model.SHA256 {
		return fmt.Errorf("checksum mismatch: got sha256 %s, expected %s", got, w.Model.SHA256)
	}
	return os.Rename(tmp, w.ModelPath)
}

type progressWriter struct {
	n       int64
	onWrite func(int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	p.onWrite(p.n)
	return len(b), nil
}

// transcribe runs whisper-cli on a 16 kHz mono WAV file and writes
// <outBase>.srt. onProgress receives percentages as whisper reports them.
// It returns the language whisper detected (or was told to use).
func (w *Whisper) transcribe(ctx context.Context, wavPath, outBase string, onProgress func(int)) (string, error) {
	cmd := exec.CommandContext(ctx, w.Bin,
		"-m", w.ModelPath,
		"-f", wavPath,
		"-t", strconv.Itoa(w.Threads),
		"-l", w.Language,
		"-osrt",
		"-of", outBase,
		"-pp",
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	cmd.Stdout = io.Discard // the transcript also echoes to stdout; the .srt file is what we keep
	if err := cmd.Start(); err != nil {
		return "", err
	}

	language := w.Language
	var tail []string // last few stderr lines, for error reporting
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		line := sc.Text()
		if m := regexpProgress.FindStringSubmatch(line); m != nil {
			if pct, err := strconv.Atoi(m[1]); err == nil {
				onProgress(pct)
			}
			continue
		}
		if m := regexpDetectedLanguage.FindStringSubmatch(line); m != nil {
			language = m[1]
		}
		tail = append(tail, line)
		if len(tail) > 8 {
			tail = tail[1:]
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		msg := strings.TrimSpace(strings.Join(tail, "\n"))
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return "", fmt.Errorf("whisper failed: %v: %s", err, msg)
	}
	return language, nil
}
