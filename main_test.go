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
