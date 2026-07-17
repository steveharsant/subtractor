package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	sessionCookieName = "subtractor_session"
	sessionTTL        = 7 * 24 * time.Hour
)

type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string]time.Time)}
}

func (s *SessionStore) Create() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand failure: " + err.Error())
	}
	token := hex.EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Opportunistically prune expired sessions.
	now := time.Now()
	for t, exp := range s.sessions {
		if exp.Before(now) {
			delete(s.sessions, t)
		}
	}
	s.sessions[token] = now.Add(sessionTTL)
	return token
}

func (s *SessionStore) Valid(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[token]
	if !ok {
		return false
	}
	if exp.Before(time.Now()) {
		delete(s.sessions, token)
		return false
	}
	return true
}

func (s *SessionStore) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// LoginLimiter applies a simple per-IP lockout against brute-force attempts.
type LoginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

const (
	limiterWindow      = 15 * time.Minute
	limiterMaxFailures = 10
)

func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{failures: make(map[string][]time.Time)}
}

func (l *LoginLimiter) Blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip)) >= limiterMaxFailures
}

func (l *LoginLimiter) RecordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[ip] = append(l.recent(ip), time.Now())
}

func (l *LoginLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

// recent must be called with the lock held; it also compacts stale entries.
func (l *LoginLimiter) recent(ip string) []time.Time {
	cutoff := time.Now().Add(-limiterWindow)
	kept := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, ip)
		return nil
	}
	l.failures[ip] = kept
	return kept
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (app *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || !app.sessions.Valid(cookie.Value) {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r)
	}
}

func (app *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if app.loginLimiter.Blocked(ip) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}

	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&creds); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	userOK := subtle.ConstantTimeCompare([]byte(creds.Username), []byte(app.username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(creds.Password), []byte(app.password)) == 1
	if !userOK || !passOK {
		app.loginLimiter.RecordFailure(ip)
		time.Sleep(500 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	app.loginLimiter.Reset(ip)
	token := app.sessions.Create()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   app.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"username": app.username})
}

func (app *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		app.sessions.Revoke(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   app.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (app *App) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"username": app.username})
}
