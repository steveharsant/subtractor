# subtractor — Development Guide

## Project overview

Python GUI app (tkinter) that uses ffmpeg/ffprobe to detect and extract text-based subtitle streams from video files. Saves output as `.txt` files alongside source videos.

## Package structure

```
subtractor/
├── __init__.py     # Package marker, __version__
├── __main__.py     # Entry point — creates tk root, launches SubtractorApp
├── core.py         # ffprobe/ffmpeg orchestration (pure logic, no GUI deps)
└── gui.py          # tkinter UI (imports core only)
```

## Key design decisions

1. **Positive allowlist for text codecs** — `TEXT_SUBTITLE_CODECS` in `core.py` is a frozenset of known text subtitle codecs. Unknown codecs are skipped (safe default). Never switch to a blocklist approach — new codecs should be explicitly vetted before they're trusted.
2. **Convert to SRT format** — extraction uses `ffmpeg -f srt`, converting all text subtitle types to SRT before writing the `.txt` file. ASS/SSA styling info is lost but text content is preserved.
3. **Threaded worker + queue** — GUI runs extraction in a `threading.Thread`, communicates progress/status back via `queue.Queue`, polled with `tk.after(100ms)`. This keeps the UI responsive during long batches.
4. **Side-by-side output** — `.txt` files land in the same directory as the source video, named by stem with optional language/index suffix.

## Testing

- **pytest** with `unittest.mock` — all ffmpeg/subprocess calls are mocked
- No ffmpeg required in CI
- `tests/test_core.py` covers: codec classification, output path computation, probe JSON parsing, extraction command construction, batch orchestration, error handling

Run: `uv run pytest -v`

## Build

- **PyInstaller** spec file: `subtractor.spec`
- Output: `dist/subtractor.exe` (single file)
- `uv run --with pyinstaller pyinstaller subtractor.spec`
- ffmpeg/ffprobe are **not** bundled — the user must have them on PATH
- UPX compression is enabled (install `upx` for smaller output)
- Use `--noconsole` for windowed-mode builds (no terminal window)

## Code style

- Type hints on all public functions
- Frozen dataclasses for value objects (`SubtitleStream`, `ProbeResult`)
- Google-style docstrings (first line is a summary)
- List-form subprocess arguments — **never** use `shell=True` (security + Unicode safety)
- Max line length: 100

## Common pitfalls

- ffmpeg must be on PATH at runtime — the app shows an error dialog if `ffprobe`/`ffmpeg` can't be found
- DVB subtitles are bitmap in practice even though ffmpeg's `codec_name` is ambiguous — they're in the bitmap set, not the text allowlist
- Windows path length limit (~260 chars) may affect deep folder structures
- `uv sync` alone won't install dev deps (pytest, pyinstaller) — use `uv sync --extra dev`
