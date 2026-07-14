# -*- mode: python ; coding: utf-8 -*-

import os
import sysconfig

block_cipher = None

# Bundle Tcl/Tk shared libraries so the binary runs on systems without
# Tcl/Tk 9.0 installed (Python 3.14+ requires Tcl/Tk 9).
_python_lib = sysconfig.get_config_var("LIBDIR")
_bundled_binaries = []
if _python_lib:
    for _so in ("libtcl9.0.so", "libtcl9tk9.0.so"):
        _path = os.path.join(_python_lib, _so)
        if os.path.isfile(_path):
            _bundled_binaries.append((_path, "."))

# Bundle ffmpeg and ffprobe if present in a "ffmpeg/" directory next to
# this spec file (look for platform-appropriate names).
_ffmpeg_dir = os.path.join(SPECPATH, "ffmpeg")
_ffmpeg_names = (
    ["ffmpeg.exe", "ffprobe.exe"] if os.name == "nt"
    else ["ffmpeg", "ffprobe"]
)
_ffmpeg_found = []
for _name in _ffmpeg_names:
    _path = os.path.join(_ffmpeg_dir, _name)
    if os.path.isfile(_path):
        _bundled_binaries.append((_path, "."))
        _ffmpeg_found.append(_name)

if _ffmpeg_found:
    print(f"  Bundling ffmpeg tools: {', '.join(_ffmpeg_found)}")
else:
    print("  WARNING: ffmpeg/ directory missing or empty — "
          "ffmpeg will NOT be bundled.")
    print("  Run: powershell -ExecutionPolicy Bypass -File "
          "scripts\\download-ffmpeg.ps1")

a = Analysis(
    [
        os.path.join("subtractor", "__init__.py"),
        os.path.join("subtractor", "__main__.py"),
        os.path.join("subtractor", "core.py"),
        os.path.join("subtractor", "gui.py"),
    ],
    pathex=[SPECPATH],
    binaries=_bundled_binaries,
    datas=[],
    hiddenimports=[],
    hookspath=[],
    runtime_hooks=[],
    excludes=[
        "tkinter.test",
        "PIL",
        "numpy",
        "matplotlib",
        "scipy",
        "curses",
        "email",
        "http.server",
    ],
    win_no_prefer_redirects=False,
    win_private_assemblies=False,
    cipher=block_cipher,
    noarchive=False,
)

pyz = PYZ(a.pure, a.zipped_data, cipher=block_cipher)

exe = EXE(
    pyz,
    a.scripts,
    a.binaries,
    a.zipfiles,
    a.datas,
    [],
    name="subtractor",
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=True,
    upx_exclude=[],
    runtime_tmpdir=None,
    console=True,
    disable_windowed_traceback=False,
    argv_emulation=False,
    target_arch=None,
    codesign_identity=None,
    entitlements_file=None,
)
