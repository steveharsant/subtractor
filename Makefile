.PHONY: sync test run build build-linux build-windows build-windows-noconsole download-ffmpeg clean

sync:
	uv sync --extra dev

test:
	uv run pytest -v

run:
	uv run subtractor

# Download ffmpeg/ffprobe binaries into ffmpeg/ for bundling.
download-ffmpeg:
	bash scripts/download-ffmpeg.sh

# Build for the current platform (single-file executable, console visible).
# Run 'make download-ffmpeg' first to bundle ffmpeg into the binary.
# Produces dist/subtractor on Linux, dist/subtractor.exe on Windows.
build:
	uv run --with pyinstaller pyinstaller subtractor.spec

# Explicit platform targets — PyInstaller always builds for the host OS,
# so these must be run on the target platform.
build-linux: build

build-windows:
	uv run --with pyinstaller pyinstaller subtractor.spec

build-windows-noconsole:
	uv run --with pyinstaller pyinstaller --noconsole subtractor.spec

clean:
	rm -rf build/ dist/ __pycache__/ subtractor/__pycache__/ tests/__pycache__/ .pytest_cache/ *.egg-info/
