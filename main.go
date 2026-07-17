// Subtractor: a web app for extracting embedded subtitles from video files.
package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

//go:embed web
var webFS embed.FS

type App struct {
	dataDir        string
	username       string
	password       string
	sessions       *SessionStore
	maxUploadBytes int64
	retention      time.Duration
	secureCookies  bool
	loginLimiter   *LoginLimiter
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadPassword() string {
	if file := os.Getenv("SUBTRACTOR_PASSWORD_FILE"); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			log.Fatalf("failed to read SUBTRACTOR_PASSWORD_FILE: %v", err)
		}
		return string(trimNewline(b))
	}
	return os.Getenv("SUBTRACTOR_PASSWORD")
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func main() {
	app := &App{
		dataDir:      envOr("SUBTRACTOR_DATA_DIR", "/data"),
		username:     envOr("SUBTRACTOR_USERNAME", "admin"),
		password:     loadPassword(),
		sessions:     NewSessionStore(),
		loginLimiter: NewLoginLimiter(),
	}

	if app.password == "" {
		log.Fatal("SUBTRACTOR_PASSWORD (or SUBTRACTOR_PASSWORD_FILE) must be set; refusing to start without credentials")
	}
	if len(app.password) < 8 {
		log.Fatal("SUBTRACTOR_PASSWORD must be at least 8 characters")
	}

	maxUploadMB, err := strconv.ParseInt(envOr("SUBTRACTOR_MAX_UPLOAD_MB", "8192"), 10, 64)
	if err != nil || maxUploadMB <= 0 {
		log.Fatal("SUBTRACTOR_MAX_UPLOAD_MB must be a positive integer")
	}
	app.maxUploadBytes = maxUploadMB << 20

	retentionDays, err := strconv.Atoi(envOr("SUBTRACTOR_RETENTION_DAYS", "14"))
	if err != nil || retentionDays <= 0 {
		log.Fatal("SUBTRACTOR_RETENTION_DAYS must be a positive integer")
	}
	app.retention = time.Duration(retentionDays) * 24 * time.Hour

	app.secureCookies = envOr("SUBTRACTOR_SECURE_COOKIES", "false") == "true"

	if err := os.MkdirAll(app.videosDir(), 0o750); err != nil {
		log.Fatalf("failed to create data directory: %v", err)
	}

	go app.retentionSweeper()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", app.handleLogin)
	mux.HandleFunc("POST /api/logout", app.handleLogout)
	mux.HandleFunc("GET /api/me", app.requireAuth(app.handleMe))
	mux.HandleFunc("POST /api/upload", app.requireAuth(app.handleUpload))
	mux.HandleFunc("GET /api/videos", app.requireAuth(app.handleListVideos))
	mux.HandleFunc("GET /api/videos/{id}", app.requireAuth(app.handleGetVideo))
	mux.HandleFunc("DELETE /api/videos/{id}", app.requireAuth(app.handleDeleteVideo))
	mux.HandleFunc("POST /api/videos/{id}/extract/{stream}", app.requireAuth(app.handleExtract))
	mux.HandleFunc("GET /api/videos/{id}/download/{stream}", app.requireAuth(app.handleDownload))

	staticFS, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("failed to mount embedded web assets: %v", err)
	}
	mux.Handle("GET /", http.FileServerFS(staticFS))

	addr := envOr("SUBTRACTOR_LISTEN", ":8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		// No global ReadTimeout: large uploads legitimately take a long time.
	}
	log.Printf("subtractor listening on %s (data dir %s, retention %s)", addr, app.dataDir, app.retention)
	log.Fatal(srv.ListenAndServe())
}

func (app *App) videosDir() string {
	return filepath.Join(app.dataDir, "videos")
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// retentionSweeper deletes stored videos older than the retention period.
func (app *App) retentionSweeper() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		app.sweepExpired()
		<-ticker.C
	}
}

func (app *App) sweepExpired() {
	entries, err := os.ReadDir(app.videosDir())
	if err != nil {
		log.Printf("retention sweep: %v", err)
		return
	}
	cutoff := time.Now().Add(-app.retention)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := loadMeta(filepath.Join(app.videosDir(), e.Name()))
		var uploaded time.Time
		if err == nil {
			uploaded = meta.UploadedAt
		} else if info, ierr := e.Info(); ierr == nil {
			// Corrupt or missing metadata: fall back to directory mtime.
			uploaded = info.ModTime()
		} else {
			continue
		}
		if uploaded.Before(cutoff) {
			dir := filepath.Join(app.videosDir(), e.Name())
			if err := os.RemoveAll(dir); err != nil {
				log.Printf("retention sweep: failed to remove %s: %v", dir, err)
			} else {
				log.Printf("retention sweep: removed expired video %s (uploaded %s)", e.Name(), uploaded.Format(time.RFC3339))
			}
		}
	}
}
