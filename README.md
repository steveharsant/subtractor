# Subtractor

A self-hosted web app for extracting (demuxing) embedded subtitles from video files.
Upload a video, see every subtitle track inside it, extract the ones you want, preview
the text in the browser, and download the result as `.srt` or `.txt`.

Ships as a **single Docker container** containing the app and ffmpeg — nothing else to
install.

## Features

- **Web UI** with drag-and-drop upload and per-file progress.
- **Single-user login** — credentials are supplied via environment variables, sessions
  are HttpOnly cookies, and failed logins are rate-limited per IP.
- **ffprobe analysis** — every embedded subtitle track is listed with its codec,
  language, title, and default/forced flags.
- **Selective extraction** — pick a track and it is demuxed with ffmpeg and shown in a
  read-only preview. Bitmap subtitle formats (PGS, VobSub, DVB) are listed but marked
  non-extractable, since converting them to text would require OCR.
- **Download as `.srt` or `.txt`** — the txt variant strips cue numbers, timestamps and
  markup.
- **14-day retention** — uploads are kept so you can log back in and re-download
  subtitles, then deleted automatically (configurable via `SUBTRACTOR_RETENTION_DAYS`).

## Quick start

```bash
docker build -t subtractor .

docker run -d \
  --name subtractor \
  -p 8080:8080 \
  -e SUBTRACTOR_USERNAME=admin \
  -e SUBTRACTOR_PASSWORD="$(< /path/to/password-file)" \
  -v subtractor-data:/data \
  subtractor
```

Then open <http://localhost:8080> and sign in.

Or with Docker Compose (reads `SUBTRACTOR_PASSWORD` from your shell environment or an
untracked `.env` file — see `.env.example`):

```bash
cp .env.example .env   # edit the password
docker compose up -d --build
```

## Configuration

All configuration is via environment variables:

| Variable | Default | Purpose |
|---|---|---|
| `SUBTRACTOR_PASSWORD` | *(required)* | Login password (min 8 chars). The app refuses to start without it. |
| `SUBTRACTOR_PASSWORD_FILE` | — | Alternative to the above: path to a file containing the password (works with Docker secrets). |
| `SUBTRACTOR_USERNAME` | `admin` | Login username. |
| `SUBTRACTOR_RETENTION_DAYS` | `14` | Days to keep uploaded videos before automatic deletion. |
| `SUBTRACTOR_MAX_UPLOAD_MB` | `8192` | Maximum upload size in MiB. |
| `SUBTRACTOR_SECURE_COOKIES` | `false` | Set `true` when serving over HTTPS so session cookies are marked `Secure`. |
| `SUBTRACTOR_LISTEN` | `:8080` | Listen address. |
| `SUBTRACTOR_DATA_DIR` | `/data` | Storage directory (mount a volume here). |

## How it works

- **Backend:** Go (standard library only — zero third-party dependencies), shelling out
  to `ffprobe` for analysis and `ffmpeg` for demuxing.
- **Frontend:** vanilla HTML/CSS/JS, embedded into the binary with `go:embed`.
- **Storage:** each upload gets a directory under `/data/videos/<id>/` holding the
  original file, a `meta.json` (probed stream info, expiry), and cached extracted
  `.srt` files. An hourly sweeper removes anything past its retention window.
- **Container:** multi-stage build; the final image is Alpine + ffmpeg + a static
  binary, running as a non-root user.

### Extraction details

Text subtitle codecs (SubRip, ASS/SSA, WebVTT, `mov_text`, …) are converted to SRT with
`ffmpeg -map 0:<index> -c:s srt`. Results are cached, so re-downloading a track later
does not re-run ffmpeg.

### Security notes

- Serve behind HTTPS (reverse proxy) in anything but a trusted LAN, and set
  `SUBTRACTOR_SECURE_COOKIES=true` when you do.
- Credentials are never baked into the image; they come from the environment or a
  secrets file at runtime.
- The container runs as a non-root user and only `/data` is writable.

## Development

Requires Go 1.23+ and ffmpeg on `PATH`:

```bash
go test ./...
SUBTRACTOR_DATA_DIR=./data SUBTRACTOR_PASSWORD=devpassword go run .
```
