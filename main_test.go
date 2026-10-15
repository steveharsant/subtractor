package main

import (
	"strings"
	"testing"
)

func TestSrtToPlainText(t *testing.T) {
	srt := "1\r\n00:00:01,000 --> 00:00:03,000\r\n<i>Hello there.</i>\r\n\r\n" +
		"2\r\n00:00:04,000 --> 00:00:06,000\r\nGeneral Kenobi!\r\nYou are bold.\r\n\r\n" +
		"3\r\n00:00:07,000 --> 00:00:09,000\r\n42\r\n"
	got := srtToPlainText(srt)
	want := "Hello there.\n\nGeneral Kenobi!\nYou are bold.\n\n42\n"
	if got != want {
		t.Errorf("srtToPlainText:\ngot  %q\nwant %q", got, want)
	}
}

func TestSrtToPlainTextStripsAssTags(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:03,000\n{\\an8}Sign text\n"
	got := srtToPlainText(srt)
	if got != "Sign text\n" {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"movie.mkv":               "movie.mkv",
		"../../etc/passwd":        "passwd",
		"..\\..\\win\\evil.mkv":   "evil.mkv",
		"":                        "upload",
		"..":                      "upload",
		"weird\x00name\x1f.mp4":   "weirdname.mp4",
		"/abs/path/to/show.s01e01.mkv": "show.s01e01.mkv",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeFilenameTruncates(t *testing.T) {
	long := strings.Repeat("a", 300) + ".mkv"
	got := sanitizeFilename(long)
	if len(got) != 200 || !strings.HasSuffix(got, ".mkv") {
		t.Errorf("expected 200-char name keeping extension, got %d chars", len(got))
	}
}

func TestSessionStore(t *testing.T) {
	s := NewSessionStore()
	token := s.Create()
	if !s.Valid(token) {
		t.Fatal("freshly created session should be valid")
	}
	if s.Valid("bogus") {
		t.Fatal("unknown token should be invalid")
	}
	s.Revoke(token)
	if s.Valid(token) {
		t.Fatal("revoked session should be invalid")
	}
}

func TestLoginLimiter(t *testing.T) {
	l := NewLoginLimiter()
	ip := "192.0.2.1"
	for i := 0; i < limiterMaxFailures; i++ {
		if l.Blocked(ip) {
			t.Fatalf("blocked too early after %d failures", i)
		}
		l.RecordFailure(ip)
	}
	if !l.Blocked(ip) {
		t.Fatal("should be blocked after max failures")
	}
	if l.Blocked("192.0.2.2") {
		t.Fatal("other IPs should not be blocked")
	}
	l.Reset(ip)
	if l.Blocked(ip) {
		t.Fatal("reset should unblock")
	}
}

func TestVideoIDPattern(t *testing.T) {
	if !videoIDPattern.MatchString(newVideoID()) {
		t.Fatal("generated IDs must match their own validation pattern")
	}
	for _, bad := range []string{"", "..", "abc", "ABCDEF012345678901234567", "a/../../../../etc/passwd"} {
		if videoIDPattern.MatchString(bad) {
			t.Errorf("pattern should reject %q", bad)
		}
	}
}

func TestSelectWhisperModel(t *testing.T) {
	cases := []struct {
		res  hostResources
		want string
	}{
		{hostResources{CPUs: 16, MemBytes: 32 * gib, AVX2: true}, "large-v3-turbo"},
		{hostResources{CPUs: 16, MemBytes: 32 * gib, AVX2: false}, "small"},
		{hostResources{CPUs: 4, MemBytes: 8 * gib, AVX2: true}, "small"},
		{hostResources{CPUs: 8, MemBytes: 2 * gib, AVX2: true}, "base"},
		{hostResources{CPUs: 1, MemBytes: 1 * gib, AVX2: true}, "tiny"},
		{hostResources{}, "tiny"},
	}
	for _, c := range cases {
		if got, _ := selectWhisperModel(c.res); got.Name != c.want {
			t.Errorf("selectWhisperModel(%+v) = %s, want %s", c.res, got.Name, c.want)
		}
	}
}

func TestCgroupParsers(t *testing.T) {
	if _, ok := parseCgroupV2CPUMax("max 100000\n"); ok {
		t.Error("unlimited cpu.max should report no limit")
	}
	if n, ok := parseCgroupV2CPUMax("150000 100000\n"); !ok || n != 2 {
		t.Errorf("1.5 CPUs should round up to 2, got %d", n)
	}
	if _, ok := parseCgroupMemLimit("max\n"); ok {
		t.Error("unlimited memory.max should report no limit")
	}
	if _, ok := parseCgroupMemLimit("9223372036854771712\n"); ok {
		t.Error("cgroup v1 'unlimited' sentinel should report no limit")
	}
	if n, ok := parseCgroupMemLimit("4294967296\n"); !ok || n != 4*gib {
		t.Errorf("got %d", n)
	}
	if got := parseMemTotal("MemTotal:       16384000 kB\nMemFree: 1 kB\n"); got != 16384000*1024 {
		t.Errorf("parseMemTotal = %d", got)
	}
	if !cpuinfoHasFlag("flags\t\t: fpu sse avx avx2 fma\n", "avx2") || cpuinfoHasFlag("flags\t\t: fpu sse avx\n", "avx2") {
		t.Error("cpuinfoHasFlag mismatch")
	}
}

func TestCombineTranscripts(t *testing.T) {
	part1 := "1\n00:00:01,000 --> 00:00:02,500\nHello.\n\n2\n00:00:03,000 --> 00:00:04,000\nPart one ends.\n"
	part2 := "1\r\n00:00:00,500 --> 00:00:02,000\r\nPart two.\r\n"
	part3 := "1\n00:00:01,000 --> 00:00:02,000\nUnknown length.\n"
	part4 := "1\n00:00:00,000 --> 00:00:01,000\nLast.\n\n2\n00:00:09,500 --> 00:00:10,800\nOverruns.\n"
	srt, txt := combineTranscripts([]transcriptPart{
		{Title: "a.mp4", Duration: 60.25, SRT: part1},
		{Title: "b.mp4", Duration: 3600, SRT: part2},
		{Title: "c.mp4", Duration: 0, SRT: part3}, // advances by its last cue end (2s)
		{Title: "d.mp4", Duration: 10, SRT: part4},
	})
	wantSRT := "1\n00:00:01,000 --> 00:00:02,500\nHello.\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\nPart one ends.\n\n" +
		"3\n00:01:00,750 --> 00:01:02,250\nPart two.\n\n" +
		"4\n01:01:01,250 --> 01:01:02,250\nUnknown length.\n\n" +
		"5\n01:01:02,250 --> 01:01:03,250\nLast.\n\n" +
		"6\n01:01:11,750 --> 01:01:12,250\nOverruns.\n\n"
	if srt != wantSRT {
		t.Errorf("combined srt:\ngot  %q\nwant %q", srt, wantSRT)
	}
	wantTXT := "=== Part 1: a.mp4 ===\n\nHello.\n\nPart one ends.\n\n" +
		"=== Part 2: b.mp4 ===\n\nPart two.\n\n" +
		"=== Part 3: c.mp4 ===\n\nUnknown length.\n\n" +
		"=== Part 4: d.mp4 ===\n\nLast.\n\nOverruns.\n"
	if txt != wantTXT {
		t.Errorf("combined txt:\ngot  %q\nwant %q", txt, wantTXT)
	}
}
