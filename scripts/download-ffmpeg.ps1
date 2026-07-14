# Download ffmpeg and ffprobe Windows binaries and place them in the
# ffmpeg\ directory so PyInstaller bundles them into subtractor.exe.
#
# Usage: powershell -ExecutionPolicy Bypass -File scripts\download-ffmpeg.ps1

$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectDir = Split-Path -Parent $ScriptDir
$FfmpegDir = Join-Path $ProjectDir "ffmpeg"

# Essential Windows builds from gyan.dev.
$FfmpegUrl = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"

Write-Host "==> Downloading ffmpeg essential build..."
$TempDir = New-TemporaryFile | ForEach-Object { Remove-Item $_; New-Item -ItemType Directory -Path $_ }
try {
    $ZipPath = Join-Path $TempDir "ffmpeg.zip"
    Invoke-WebRequest -Uri $FfmpegUrl -OutFile $ZipPath

    Write-Host "==> Extracting..."
    Expand-Archive -Path $ZipPath -DestinationPath $TempDir

    # The zip contains a single directory like "ffmpeg-7.1.1-essentials_build".
    $FfmpegSrcDir = Get-ChildItem -Path $TempDir -Directory | Select-Object -First 1
    $BinDir = Join-Path $FfmpegSrcDir.FullName "bin"

    New-Item -ItemType Directory -Force -Path $FfmpegDir | Out-Null
    Copy-Item -Path (Join-Path $BinDir "ffmpeg.exe") -Destination (Join-Path $FfmpegDir "ffmpeg.exe")
    Copy-Item -Path (Join-Path $BinDir "ffprobe.exe") -Destination (Join-Path $FfmpegDir "ffprobe.exe")

    Write-Host "==> Done. Binaries placed in $FfmpegDir\"
    Get-ChildItem -Path $FfmpegDir | ForEach-Object { Write-Host "    $($_.Name)  $('{0:N0}' -f $_.Length) bytes" }
    Write-Host ""
    Write-Host "You can now run: uv run --with pyinstaller pyinstaller subtractor.spec"
} finally {
    Remove-Item -Recurse -Force $TempDir
}
