# Subtractor

A self-hosted web app for getting subtitles out of video files. Upload a video and
either extract (demux) the subtitle tracks embedded in it, or generate a transcript from
its audio with [Whisper](https://github.com/ggml-org/whisper.cpp). Preview the text in the
browser and download it as `.srt` or `.txt`.

Ships as a **single Docker container** containing the app, ffmpeg and whisper.cpp — nothing
else to install. The Whisper model is downloaded on first start.

## Features

- **Web UI** with drag-and-drop upload of one or many files, with progress.
- **Single-user login** — credentials are supplied via environment variables, sessions
  are HttpOnly cookies, and failed logins are rate-limited per IP.
- **ffprobe analysis** — every embedded subtitle track is listed with its codec,
  language, title, and default/forced flags.
- **Selective extraction** — pick a track and it is demuxed with ffmpeg and shown in a
  read-only preview. Bitmap subtitle formats (PGS, VobSub, DVB) are listed but marked
  non-extractable, since converting them to text would require OCR.
- **Whisper speech-to-text** — tick "Generate a transcript with Whisper" when uploading,
  or press "Generate transcript" on any video. Jobs run one at a time in the background,
  show live progress, and survive container restarts.
- **Combined transcripts** — upload several files (e.g. a lecture split into parts), put
  them in order, and tick "Combine all files into a single transcript". Each part is
  transcribed, then joined into one SRT with continuous timestamps (part 2 starts where
  part 1 ends) and one `.txt` with a heading per part.
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
| `SUBTRACTOR_WHISPER_MODEL` | `auto` | `auto`, or one of `tiny`, `base`, `small`, `medium`, `large-v3-turbo`, `large-v3`. |
| `SUBTRACTOR_WHISPER_LANGUAGE` | `auto` | Spoken language code (e.g. `en`), or `auto` to detect per video. |
| `SUBTRACTOR_WHISPER_THREADS` | CPUs, max 16 | Threads whisper.cpp uses. |
| `SUBTRACTOR_WHISPER_MODEL_BASE_URL` | Hugging Face, pinned revision | Mirror to download models from. Checksums are still verified. |

### Whisper model selection

With `SUBTRACTOR_WHISPER_MODEL=auto`, the app checks what the container can use at
startup — CPU count and total RAM, both capped by Docker/cgroup limits such as `--cpus`
and `--memory`, plus AVX2 support — and picks the best model that runs at a usable speed
on CPU:

| Model | Download | Picked when |
|---|---|---|
| `large-v3-turbo` | 1.6 GB | ≥ 8 CPUs, ≥ 6 GiB RAM, AVX2 |
| `small` | 465 MB | ≥ 4 CPUs, ≥ 3 GiB RAM |
| `base` | 141 MB | ≥ 2 CPUs, ≥ 1.5 GiB RAM |
| `tiny` | 74 MB | otherwise |

`medium` and `large-v3` are never auto-selected (turbo is faster and close in accuracy)
but can be set explicitly. The choice and the reason are shown in the UI.

On first start the model downloads into `/data/models` in the background; the UI shows
progress, and transcripts queue until it finishes. Downloads are checked against
pinned SHA-256 checksums and retried with backoff on failure. To run offline, place the
`ggml-<model>.bin` file in `/data/models` yourself.

## How it works

- **Backend:** Go (standard library only — zero third-party Go dependencies), shelling
  out to `ffprobe` for analysis, `ffmpeg` for demuxing and audio extraction, and
  `whisper-cli` for speech-to-text.
- **Frontend:** vanilla HTML/CSS/JS, embedded into the binary with `go:embed`.
- **Storage:** each upload gets a directory under `/data/videos/<id>/` holding the
  original file, a `meta.json` (probed stream info, transcript state, expiry), and cached
  extracted `.srt` files (including `whisper.srt`). Combined transcripts are small
  `/data/collections/<id>.json` files listing their parts; the joined output is built on
  download. Deleting a combined transcript deletes its parts. An hourly sweeper removes
  anything past its retention window. Models live in `/data/models` and are kept.
- **Container:** multi-stage build. whisper.cpp (pinned to `v1.9.4`) is compiled with one
  CPU backend per instruction-set level, and the fastest one the host supports is loaded
  at runtime. The final image (~160 MB) is Alpine + ffmpeg + whisper.cpp + a static Go
  binary, running as a non-root user. CPU only.

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

Requires Go 1.23+ and ffmpeg on `PATH`. Speech-to-text also needs `whisper-cli` on
`PATH` (or `SUBTRACTOR_WHISPER_BIN`); without it the app runs with transcription disabled.

```bash
go test ./...
SUBTRACTOR_DATA_DIR=./data SUBTRACTOR_PASSWORD=devpassword go run .
```
