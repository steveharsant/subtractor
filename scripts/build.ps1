# Build subtractor.exe — downloads ffmpeg, installs Python deps, builds.
#
# Usage: powershell -ExecutionPolicy Bypass -File scripts\build.ps1
#
# To build without a console window add: -NoConsole

param(
    [switch] $NoConsole
)

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectDir = Split-Path -Parent $ScriptDir

Set-Location $ProjectDir

# ---- Step 1: download ffmpeg -----------------------------------------------
Write-Host "=== Step 1/3: Downloading ffmpeg ===" -ForegroundColor Cyan
& "$ScriptDir\download-ffmpeg.ps1"

# ---- Step 2: install dependencies ------------------------------------------
Write-Host "=== Step 2/3: Installing Python dependencies ===" -ForegroundColor Cyan
uv sync --extra dev
if ($LASTEXITCODE -ne 0) { throw "uv sync failed" }

# ---- Step 3: build ---------------------------------------------------------
Write-Host "=== Step 3/3: Building subtractor.exe ===" -ForegroundColor Cyan
if ($NoConsole) {
    uv run --with pyinstaller pyinstaller --noconsole subtractor.spec
} else {
    uv run --with pyinstaller pyinstaller subtractor.spec
}
if ($LASTEXITCODE -ne 0) { throw "PyInstaller build failed" }

Write-Host ""
Write-Host "=== Done ===" -ForegroundColor Green
Get-Item "$ProjectDir\dist\subtractor.exe" | ForEach-Object {
    Write-Host "Binary: $($_.FullName)  $('{0:N0}' -f $_.Length) bytes"
}
