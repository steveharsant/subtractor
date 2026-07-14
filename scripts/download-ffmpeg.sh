#!/usr/bin/env bash
# Download ffmpeg and ffprobe static binaries and place them in the
# ffmpeg/ directory so PyInstaller bundles them into the subtractor binary.
#
# Usage: bash scripts/download-ffmpeg.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
FFMPEG_DIR="$PROJECT_DIR/ffmpeg"

# Linux static builds from johnvansickle.com (amd64).
FFMPEG_URL="https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-amd64-static.tar.xz"

echo "==> Downloading ffmpeg static build..."
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

curl -fsSL --retry 3 -o "$TMP_DIR/ffmpeg.tar.xz" "$FFMPEG_URL"
echo "==> Extracting..."
tar -xf "$TMP_DIR/ffmpeg.tar.xz" -C "$TMP_DIR"

# The tarball contains a single directory like "ffmpeg-7.1.1-amd64-static".
FFMPEG_SRC_DIR="$(find "$TMP_DIR" -maxdepth 1 -type d -name 'ffmpeg-*-amd64-static' | head -1)"

if [ -z "$FFMPEG_SRC_DIR" ]; then
    echo "ERROR: Could not find extracted ffmpeg directory."
    exit 1
fi

mkdir -p "$FFMPEG_DIR"
cp "$FFMPEG_SRC_DIR/ffmpeg" "$FFMPEG_DIR/ffmpeg"
cp "$FFMPEG_SRC_DIR/ffprobe" "$FFMPEG_DIR/ffprobe"
chmod +x "$FFMPEG_DIR/ffmpeg" "$FFMPEG_DIR/ffprobe"

echo "==> Done. Binaries placed in $FFMPEG_DIR/"
ls -lh "$FFMPEG_DIR/ffmpeg" "$FFMPEG_DIR/ffprobe"
echo ""
echo "You can now run: make build"
