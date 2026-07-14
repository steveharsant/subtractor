"""tkinter GUI for subtractor — file selection, probing, and extraction."""

from __future__ import annotations

import queue
import threading
import tkinter as tk
from pathlib import Path
from tkinter import messagebox, ttk

from subtractor import __version__
from subtractor.core import extract_all, find_ffmpeg_tool, is_text_subtitle, probe_video

VIDEO_EXTENSIONS: frozenset[str] = frozenset({
    ".mkv", ".mp4", ".avi", ".mov", ".wmv", ".m4v", ".ts", ".mts", ".m2ts", ".ogm",
})


class SubtractorApp:
    """Main application window."""

    def __init__(self, master: tk.Tk) -> None:
        self.master = master
        master.title("subtractor — SUBtitle exTRACTOR")
        master.geometry("700x480")
        master.minsize(500, 350)

        # --- State ---
        # List of (Path | None, str) — path + display label.
        self._files: list[tuple[Path, str]] = []
        self._extracting = False
        self._stop_requested = False
        self._queue: queue.Queue[dict] = queue.Queue()

        # --- Menu bar ---
        menubar = tk.Menu(master)
        file_menu = tk.Menu(menubar, tearoff=0)
        file_menu.add_command(label="Exit", command=self._on_close)
        menubar.add_cascade(label="File", menu=file_menu)
        help_menu = tk.Menu(menubar, tearoff=0)
        help_menu.add_command(label="About", command=self._show_about)
        menubar.add_cascade(label="Help", menu=help_menu)
        master.config(menu=menubar)

        # --- Main frame ---
        main = ttk.Frame(master, padding=12)
        main.pack(fill=tk.BOTH, expand=True)

        # File list label
        ttk.Label(main, text="Video files:").pack(anchor=tk.W)

        # Listbox + scrollbar
        list_frame = ttk.Frame(main)
        list_frame.pack(fill=tk.BOTH, expand=True, pady=(4, 4))

        self._listbox = tk.Listbox(
            list_frame, selectmode=tk.EXTENDED, activestyle="none",
            exportselection=False,
        )
        self._listbox.pack(side=tk.LEFT, fill=tk.BOTH, expand=True)

        scrollbar = ttk.Scrollbar(list_frame, orient=tk.VERTICAL, command=self._listbox.yview)
        scrollbar.pack(side=tk.RIGHT, fill=tk.Y)
        self._listbox.config(yscrollcommand=scrollbar.set)

        # Button bar
        btn_frame = ttk.Frame(main)
        btn_frame.pack(fill=tk.X, pady=(0, 8))

        self._add_btn = ttk.Button(btn_frame, text="Add Files...", command=self.add_files)
        self._add_btn.pack(side=tk.LEFT, padx=(0, 4))
        self._folder_btn = ttk.Button(btn_frame, text="Add Folder...", command=self.add_folder)
        self._folder_btn.pack(side=tk.LEFT, padx=(0, 4))
        self._remove_btn = ttk.Button(btn_frame, text="Remove Selected", command=self.remove_selected)
        self._remove_btn.pack(side=tk.LEFT, padx=(0, 4))
        self._clear_btn = ttk.Button(btn_frame, text="Clear All", command=self.clear_all)
        self._clear_btn.pack(side=tk.LEFT)

        # Progress bar
        self._progress = ttk.Progressbar(main, mode="determinate")
        self._progress.pack(fill=tk.X, pady=(0, 4))

        # Status label
        self._status_var = tk.StringVar(value="Ready")
        status_label = ttk.Label(main, textvariable=self._status_var, anchor=tk.W)
        status_label.pack(fill=tk.X)

        # Extract button
        self._extract_btn = ttk.Button(
            main, text="Extract Subtitles", command=self.start_extraction,
        )
        self._extract_btn.pack(pady=(8, 0))

        # --- Window close handler ---
        master.protocol("WM_DELETE_WINDOW", self._on_close)

    # -- File management ----------------------------------------------------

    def add_files(self) -> None:
        """Open a multi-file picker and add selected video files."""
        from tkinter import filedialog

        paths = filedialog.askopenfilenames(
            title="Select video files",
            filetypes=[
                ("Video files", "*.mkv *.mp4 *.avi *.mov *.wmv *.m4v *.ts *.mts *.m2ts *.ogm"),
                ("All files", "*.*"),
            ],
        )
        self._append_files([Path(p) for p in paths])

    def add_folder(self) -> None:
        """Open a folder picker and recursively add video files."""
        from tkinter import filedialog

        folder = filedialog.askdirectory(title="Select folder with video files")
        if not folder:
            return
        root = Path(folder)
        paths = [
            p for p in root.rglob("*")
            if p.suffix.lower() in VIDEO_EXTENSIONS and p.is_file()
        ]
        if not paths:
            messagebox.showinfo(
                "No videos found",
                f"No video files found in:\n{folder}",
            )
            return
        self._append_files(paths)

    def remove_selected(self) -> None:
        """Remove selected entries from the file list."""
        selected = set(self._listbox.curselection())
        if not selected:
            return
        self._files = [
            item for i, item in enumerate(self._files) if i not in selected
        ]
        self._refresh_listbox()

    def clear_all(self) -> None:
        """Remove all files from the list."""
        if not self._files:
            return
        self._files.clear()
        self._refresh_listbox()

    # -- Extraction ---------------------------------------------------------

    def start_extraction(self) -> None:
        """Validate state and launch the extraction worker thread."""
        if self._extracting:
            return

        valid = [(p, l) for p, l in self._files if p is not None]
        if not valid:
            messagebox.showwarning(
                "No files",
                "Add at least one video file before extracting.",
            )
            return

        self._extracting = True
        self._stop_requested = False
        self._set_ui_enabled(False)
        self._status_var.set("Starting...")
        self._progress.config(value=0, maximum=len(valid))

        paths = [p for p, _ in valid]
        thread = threading.Thread(
            target=self._extraction_worker,
            args=(paths,),
            daemon=True,
        )
        thread.start()
        self.master.after(100, self._poll_queue)

    def _extraction_worker(self, paths: list[Path]) -> None:
        """Background thread: runs extract_all and sends messages via queue."""
        try:
            extract_all(
                paths,
                progress_callback=self._on_progress,
                status_callback=self._on_status,
            )
            self._queue.put({"type": "done"})
        except Exception as exc:
            self._queue.put({"type": "error", "message": str(exc)})

    def _on_progress(self, current: int, total: int) -> None:
        self._queue.put({"type": "progress", "current": current, "total": total})

    def _on_status(self, message: str) -> None:
        self._queue.put({"type": "status", "text": message})

    def _poll_queue(self) -> None:
        """Process messages from the worker thread (called via after())."""
        try:
            while True:
                msg = self._queue.get_nowait()
                self._handle_message(msg)
        except queue.Empty:
            pass

        if self._extracting:
            self.master.after(100, self._poll_queue)

    def _handle_message(self, msg: dict) -> None:
        msg_type = msg["type"]

        if msg_type == "progress":
            self._progress.config(value=msg["current"])
            current = msg["current"]
            total = msg["total"]
            self._status_var.set(f"Processed {current} of {total} file(s)")
        elif msg_type == "status":
            self._status_var.set(msg["text"])
        elif msg_type == "done":
            self._finish_extraction(success=True)
        elif msg_type == "error":
            self._finish_extraction(success=False, error=msg.get("message"))

    def _finish_extraction(
        self, success: bool, error: str | None = None
    ) -> None:
        self._extracting = False
        self._set_ui_enabled(True)

        if error:
            messagebox.showerror(
                "Extraction Error",
                f"An unexpected error occurred:\n\n{error}",
            )
            self._status_var.set("Error — see details above")
        else:
            self._progress.config(value=self._progress.cget("maximum"))
            count = len(self._files)
            self._status_var.set(
                f"Done — processed {count} file(s). "
                f"Subtitles saved alongside source videos."
            )
            messagebox.showinfo(
                "Extraction Complete",
                f"Finished processing {count} file(s).\n\n"
                "Extracted subtitles were saved as .txt files\n"
                "in the same directories as the source videos.",
            )

    # -- Internal helpers ---------------------------------------------------

    def _check_ffmpeg(self) -> bool:
        """Return True if ffprobe/ffmpeg are on PATH, show warning if not."""
        try:
            find_ffmpeg_tool("ffprobe")
            return True
        except RuntimeError as e:
            messagebox.showwarning(
                "ffmpeg not found",
                f"{e}\n\n"
                "Download ffmpeg from https://ffmpeg.org/download.html\n"
                "and ensure ffmpeg.exe and ffprobe.exe are on your PATH.",
            )
            return False

    def _append_files(self, paths: list[Path]) -> None:
        if not paths:
            return

        if not self._check_ffmpeg():
            return

        existing = {p.resolve() for p, _ in self._files}

        new: list[tuple[Path, str]] = []
        for p in paths:
            resolved = p.resolve()
            if resolved in existing:
                continue
            existing.add(resolved)
            label = self._format_file_label(p)
            new.append((p, label))

        if new:
            self._files.extend(new)
            self._refresh_listbox()

    def _format_file_label(self, path: Path) -> str:
        """Probe the file and return a display label showing subtitle info."""
        try:
            probe = probe_video(path)
        except RuntimeError as exc:
            msg = str(exc)
            if "not found on PATH" in msg:
                return f"{path.name}  [ffmpeg/ffprobe not found on PATH]"
            return f"{path.name}  [probe failed: {msg}]"
        except Exception as exc:
            return f"{path.name}  [probe failed: {exc}]"

        if not probe.streams:
            return f"{path.name}  [no text subtitles]"

        parts: list[str] = []
        for s in probe.streams:
            tag = s.language or str(s.index)
            parts.append(f"{tag}({s.codec})")
        return f"{path.name}  [{', '.join(parts)}]"

    def _refresh_listbox(self) -> None:
        self._listbox.delete(0, tk.END)
        for _, label in self._files:
            self._listbox.insert(tk.END, label)

    def _set_ui_enabled(self, enabled: bool) -> None:
        state = tk.NORMAL if enabled else tk.DISABLED
        self._add_btn.config(state=state)
        self._folder_btn.config(state=state)
        self._remove_btn.config(state=state)
        self._clear_btn.config(state=state)
        self._extract_btn.config(state=state)
        if enabled:
            self._extract_btn.config(text="Extract Subtitles")
        else:
            self._extract_btn.config(text="Extracting...")

    def _show_about(self) -> None:
        messagebox.showinfo(
            "About subtractor",
            f"subtractor v{__version__}\n\n"
            "SUBtitle exTRACTOR\n\n"
            "Batch extract text subtitles from video files\n"
            "using ffmpeg.\n\n"
            "License: GPL-3.0-or-later",
        )

    def _on_close(self) -> None:
        if self._extracting:
            confirm = messagebox.askyesno(
                "Extraction in progress",
                "Extraction is still running. Stop and exit?",
            )
            if not confirm:
                return
            self._stop_requested = True
        self.master.destroy()
