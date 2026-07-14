# subtractor

A simple GUI tool to batch-extract text subtitles from video files using ffmpeg.

## Features

- **Batch processing** — select multiple video files or entire folders
- **Automatic subtitle detection** — probes each file for text-based subtitle streams (SRT, ASS, SSA, WebVTT, MOV_TEXT)
- **Smart output naming** — language-tagged filenames for multi-track files
- **Bitmap filtering** — automatically skips PGS/VobSub/DVB bitmap subtitles
- **Single-file executable** — distributable binaries for Windows and Linux with no Python installation required
- **Responsive GUI** — threaded extraction keeps the UI interactive during processing

## Prerequisites

- **[ffmpeg](https://ffmpeg.org/)** — `ffmpeg` and `ffprobe` must be on your system **PATH**

## Installation

### Option 1: Standalone executable

Download the binary for your platform from the [Releases](https://github.com/steveharsant/subtractor/releases) page:

- **Windows**: `subtractor.exe`
- **Linux**: `subtractor`

Requires ffmpeg on PATH.

### Option 2: From source with uv

```bash
git clone https://github.com/steveharsant/subtractor.git
cd subtractor
uv sync --extra dev
uv run subtractor
```

### Option 3: From source with pip

```bash
git clone https://github.com/steveharsant/subtractor.git
cd subtractor
pip install -e .
subtractor
```

## Usage

1. Launch subtractor
2. Click **Add Files...** to select individual video files, or **Add Folder...** to scan a directory recursively
3. Review the detected subtitle streams shown next to each file
4. Click **Extract Subtitles**
5. Find the `.txt` files alongside your source videos

## Output naming

| Scenario | Filename |
|---|---|
| Single subtitle track | `movie.txt` |
| Multiple tracks (with language tags) | `movie.eng.txt`, `movie.spa.txt` |
| Multiple tracks (same language) | `movie.eng_0.txt`, `movie.eng_3.txt` |
| Multiple tracks (no language tags) | `movie.0.txt`, `movie.1.txt` |

## Supported subtitle codecs

| Codec | Description |
|---|---|
| `subrip` / `srt` | SubRip (SRT) — most common text format |
| `ass` | Advanced SubStation Alpha |
| `ssa` | SubStation Alpha |
| `webvtt` | Web Video Text Tracks |
| `mov_text` | MP4/MOV timed text |
| `text` | Raw text subtitles |

Bitmap formats (PGS, VobSub, DVB, XSUB) are detected but intentionally skipped. Support for these will come in a future release.

## Building from source

PyInstaller builds for the host platform — run on Linux to get a Linux binary, on Windows to get a Windows binary.

```bash
# Install dependencies (including PyInstaller)
uv sync --extra dev

# Build single-file executable for the current platform
make build

# Windows only: build without console window
make build-windows-noconsole

# Or directly:
uv run --with pyinstaller pyinstaller subtractor.spec
```

The output is `dist/subtractor` on Linux, `dist/subtractor.exe` on Windows.

To reduce the executable size further, install [UPX](https://upx.github.io/) before building — PyInstaller will use it automatically when the `upx=True` option is set in the spec file.

## Development

```bash
uv sync --extra dev   # install with dev dependencies
uv run pytest -v      # run tests
uv run subtractor     # run the app
```

Tests use mocked subprocess calls — ffmpeg is not required in CI.

## License

GPL-3.0-or-later — see [LICENSE](LICENSE).
