# -*- mode: python ; coding: utf-8 -*-

import os
import sysconfig

block_cipher = None

# Bundle Tcl/Tk shared libraries so the binary runs on systems without
# Tcl/Tk 9.0 installed (Python 3.14+ requires Tcl/Tk 9).
_python_lib = sysconfig.get_config_var("LIBDIR")
_tcl_binaries = []
if _python_lib:
    for _so in ("libtcl9.0.so", "libtcl9tk9.0.so"):
        _path = os.path.join(_python_lib, _so)
        if os.path.isfile(_path):
            _tcl_binaries.append((_path, "."))

a = Analysis(
    [
        os.path.join("subtractor", "__init__.py"),
        os.path.join("subtractor", "__main__.py"),
        os.path.join("subtractor", "core.py"),
        os.path.join("subtractor", "gui.py"),
    ],
    pathex=[SPECPATH],
    binaries=_tcl_binaries,
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
