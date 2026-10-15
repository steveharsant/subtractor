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
	"sync"
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

	metaMu      sync.Mutex // serialises meta.json read-modify-write cycles
	whisper     *Whisper
	transcriber *Transcriber
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

	for _, dir := range []string{app.videosDir(), app.collectionsDir()} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			log.Fatalf("failed to create data directory: %v", err)
		}
	}

	app.whisper, err = NewWhisper(app.dataDir)
	if err != nil {
		log.Fatal(err)
	}
	app.whisper.Start() // downloads the model in the background on first run
	app.transcriber = NewTranscriber(app)
	app.transcriber.Resume()
	go app.transcriber.Run()

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
	mux.HandleFunc("POST /api/videos/{id}/transcribe", app.requireAuth(app.handleTranscribe))
	mux.HandleFunc("GET /api/videos/{id}/transcript", app.requireAuth(app.handleGetTranscript))
	mux.HandleFunc("GET /api/videos/{id}/transcript/download", app.requireAuth(app.handleDownloadTranscript))
	mux.HandleFunc("GET /api/whisper", app.requireAuth(app.handleWhisperStatus))
	mux.HandleFunc("POST /api/collections", app.requireAuth(app.handleCreateCollection))
	mux.HandleFunc("GET /api/collections", app.requireAuth(app.handleListCollections))
	mux.HandleFunc("GET /api/collections/{id}", app.requireAuth(app.handleGetCollection))
	mux.HandleFunc("DELETE /api/collections/{id}", app.requireAuth(app.handleDeleteCollection))
	mux.HandleFunc("GET /api/collections/{id}/transcript", app.requireAuth(app.handleGetCollectionTranscript))
	mux.HandleFunc("GET /api/collections/{id}/download", app.requireAuth(app.handleDownloadCollection))

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
			app.transcriber.Cancel(e.Name())
			if err := os.RemoveAll(dir); err != nil {
				log.Printf("retention sweep: failed to remove %s: %v", dir, err)
			} else {
				log.Printf("retention sweep: removed expired video %s (uploaded %s)", e.Name(), uploaded.Format(time.RFC3339))
			}
		}
	}

	// Collections expire with their parts; also drop ones whose parts are all gone.
	collections, err := app.listCollections()
	if err != nil {
		return
	}
	for _, c := range collections {
		alive := 0
		for _, id := range c.VideoIDs {
			if _, err := app.videoDir(id); err == nil {
				alive++
			}
		}
		if c.ExpiresAt.Before(time.Now()) || alive == 0 {
			if err := app.deleteCollection(c); err != nil {
				log.Printf("retention sweep: failed to remove collection %s: %v", c.ID, err)
			} else {
				log.Printf("retention sweep: removed collection %s (%q)", c.ID, c.Name)
			}
		}
	}
}
