"""Core ffprobe/ffmpeg orchestration for subtitle extraction.

Pure logic module — no GUI dependencies. All subprocess interaction is
contained here so it can be unit-tested with mocking.
"""

from __future__ import annotations

import json
import logging
import os
import subprocess
import sys
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from pathlib import Path

logger = logging.getLogger(__name__)

# -- Text subtitle codec allowlist -------------------------------------------
# Only codecs in this set are considered text-based and safe to extract.
# Everything else (including bitmap types) is skipped until explicitly added.

TEXT_SUBTITLE_CODECS: frozenset[str] = frozenset({
    "subrip",      # SRT
    "srt",         # Alternate SRT identifier
    "ass",         # Advanced SubStation Alpha
    "ssa",         # SubStation Alpha
    "webvtt",      # Web Video Text Tracks
    "mov_text",    # MP4/MOV timed text
    "text",        # Raw text
})

# Documented for diagnostics — not used in filtering logic.
# fmt: off
BITMAP_SUBTITLE_CODECS: frozenset[str] = frozenset({
    "dvd_subtitle", "dvdsub", "hdmv_pgs_subtitle", "pgssub",
    "xsub", "dvb_subtitle", "dvb_teletext",
})
# fmt: on


# -- Data types --------------------------------------------------------------


@dataclass(frozen=True)
class SubtitleStream:
    """Metadata for a single text subtitle stream within a video file."""

    index: int
    codec: str
    language: str | None = None
    title: str | None = None


@dataclass(frozen=True)
class ProbeResult:
    """Result of probing a single video file for subtitle streams."""

    path: Path
    streams: tuple[SubtitleStream, ...]
    duration_seconds: float | None = None


# -- Tool discovery ----------------------------------------------------------


def find_ffmpeg_tool(name: str) -> str:
    """Resolve the path to *ffmpeg* or *ffprobe*.

    Search order:

    1. Same directory as the running executable (so ffmpeg can be placed
       alongside the subtractor binary).
    2. PyInstaller bundle directory (``sys._MEIPASS``).
    3. Current working directory.
    4. System ``PATH``.

    Raises :exc:`RuntimeError` if the tool cannot be found.
    """
    exe_name = f"{name}.exe" if os.name == "nt" else name

    import shutil

    # Search directories in priority order.
    search_dirs: list[str] = []

    # 1. Directory containing the running executable.
    exe_dir = os.path.dirname(sys.executable)
    if exe_dir:
        search_dirs.append(exe_dir)

    # 2. PyInstaller one-file bundle extraction directory.
    bundle_dir = getattr(sys, "_MEIPASS", None)
    if bundle_dir:
        search_dirs.append(bundle_dir)

    # 3. Current working directory.
    search_dirs.append(os.getcwd())

    for directory in search_dirs:
        candidate = os.path.join(directory, exe_name)
        if os.path.isfile(candidate):
            return candidate

    # 4. Fall back to PATH.
    path = shutil.which(exe_name)
    if path is None:
        raise RuntimeError(
            f"{name} not found. Place {exe_name} alongside the subtractor "
            f"binary, in the current directory, or on your system PATH. "
            f"Download from https://ffmpeg.org/download.html"
        )
    return path


# -- Codec classification ----------------------------------------------------


def is_text_subtitle(codec_name: str) -> bool:
    """Return ``True`` if *codec_name* is a known text-based subtitle codec."""
    return codec_name.lower() in TEXT_SUBTITLE_CODECS


# -- Probing ----------------------------------------------------------------


def probe_video(path: Path) -> ProbeResult:
    """Run ffprobe against *path* and return detected text subtitle streams.

    Only streams whose *codec_name* passes :func:`is_text_subtitle` are
    included.  Bitmap and unknown codecs are silently skipped (logged at
    ``DEBUG`` level when they appear).

    Raises:
        FileNotFoundError: *path* does not exist.
        RuntimeError: ffprobe is unavailable or returned an error.
    """
    if not path.is_file():
        raise FileNotFoundError(f"Video file not found: {path}")

    ffprobe_bin = find_ffmpeg_tool("ffprobe")

    cmd = [
        ffprobe_bin,
        "-v", "quiet",
        "-print_format", "json",
        "-show_streams",
        str(path),
    ]

    try:
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
    except subprocess.CalledProcessError as exc:
        raise RuntimeError(
            f"ffprobe failed for {path.name}: {exc.stderr.strip()}"
        ) from exc

    try:
        data = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(
            f"Failed to parse ffprobe output for {path.name}: {exc}"
        ) from exc

    streams: list[SubtitleStream] = []
    duration: float | None = None

    fmt_info = data.get("format", {})
    if fmt_info.get("duration"):
        try:
            duration = float(fmt_info["duration"])
        except (TypeError, ValueError):
            duration = None

    for stream in data.get("streams", []):
        if stream.get("codec_type") != "subtitle":
            continue

        codec = stream.get("codec_name", "").lower()
        if not codec:
            continue

        if is_text_subtitle(codec):
            tags = stream.get("tags", {}) or {}
            streams.append(
                SubtitleStream(
                    index=stream["index"],
                    codec=codec,
                    language=tags.get("language"),
                    title=tags.get("title"),
                )
            )
        else:
            logger.debug(
                "Skipping non-text subtitle stream #%d (codec=%s) in %s",
                stream.get("index", -1),
                codec,
                path.name,
            )

    logger.info(
        "Probed %s: %d text subtitle stream(s) found",
        path.name,
        len(streams),
    )
    return ProbeResult(path=path, streams=tuple(streams), duration_seconds=duration)


# -- Output path computation -------------------------------------------------


def get_output_path(
    video_path: Path,
    stream: SubtitleStream,
    all_streams: Sequence[SubtitleStream],
) -> Path:
    """Determine the output ``.txt`` path for a subtitle stream.

    Naming rules (applied in order):

    1. Single text stream → ``<stem>.txt``
    2. Multiple streams *with* language tags → ``<stem>.<lang>.txt``
    3. Language collision → ``<stem>.<lang>_<index>.txt``
    4. Multiple streams *without* language tags → ``<stem>.<index>.txt``
    """
    parent = video_path.parent
    stem = video_path.stem

    if len(all_streams) == 1:
        return parent / f"{stem}.txt"

    if stream.language:
        # Check for collisions — same language used by multiple streams.
        same_lang = [s for s in all_streams if s.language == stream.language]
        if len(same_lang) > 1:
            return parent / f"{stem}.{stream.language}_{stream.index}.txt"
        return parent / f"{stem}.{stream.language}.txt"

    return parent / f"{stem}.{stream.index}.txt"


# -- Extraction --------------------------------------------------------------


def extract_subtitle(
    video_path: Path,
    stream: SubtitleStream,
    output_path: Path,
) -> None:
    """Extract a single subtitle stream to a ``.txt`` file via ffmpeg.

    The output is always SRT-format text regardless of the source codec.
    """
    ffmpeg_bin = find_ffmpeg_tool("ffmpeg")

    cmd = [
        ffmpeg_bin,
        "-y",
        "-i", str(video_path),
        "-map", f"0:s:{stream.index}",
        "-f", "srt",
        str(output_path),
    ]

    try:
        subprocess.run(cmd, capture_output=True, text=True, check=True)
    except subprocess.CalledProcessError as exc:
        raise RuntimeError(
            f"ffmpeg failed extracting stream #{stream.index} "
            f"from {video_path.name}: {exc.stderr.strip()}"
        ) from exc

    logger.info("Extracted %s → %s", video_path.name, output_path.name)


# -- Batch orchestration ----------------------------------------------------


def extract_all(
    video_paths: Sequence[Path],
    progress_callback: Callable[[int, int], None] | None = None,
    status_callback: Callable[[str], None] | None = None,
) -> dict[Path, list[Path]]:
    """Batch-extract text subtitles from *video_paths*.

    Parameters:
        video_paths: One or more video files to process.
        progress_callback: Called with ``(current, total)`` after each file.
        status_callback: Called with a human-readable status message.
        stop_check: Optional callable — return ``True`` to abort early.

    Returns:
        Mapping of ``{video_path: [output_paths]}`` for successful extractions.
        Files with zero text subtitles or errors are omitted from the result.
    """
    results: dict[Path, list[Path]] = {}
    total = len(video_paths)

    for idx, video_path in enumerate(video_paths):
        if status_callback:
            status_callback(f"Probing {video_path.name}...")

        try:
            probe = probe_video(video_path)
        except (FileNotFoundError, RuntimeError) as exc:
            logger.warning("Skipping %s: %s", video_path.name, exc)
            if progress_callback:
                progress_callback(idx + 1, total)
            continue

        if not probe.streams:
            logger.info("No text subtitle streams in %s — skipping", video_path.name)
            if progress_callback:
                progress_callback(idx + 1, total)
            continue

        outputs: list[Path] = []
        for stream in probe.streams:
            output_path = get_output_path(video_path, stream, probe.streams)
            if status_callback:
                status_callback(
                    f"Extracting stream #{stream.index} "
                    f"({stream.codec}) from {video_path.name}..."
                )

            try:
                extract_subtitle(video_path, stream, output_path)
                outputs.append(output_path)
            except RuntimeError as exc:
                logger.warning(
                    "Failed to extract stream #%d from %s: %s",
                    stream.index,
                    video_path.name,
                    exc,
                )
                # Continue with remaining streams in this file.

        if outputs:
            results[video_path] = outputs

        if progress_callback:
            progress_callback(idx + 1, total)

    return results
