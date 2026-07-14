"""subtractor — SUBtitle exTRACTOR

Entry point. Launches the tkinter GUI.
"""

from subtractor.gui import SubtractorApp


def main() -> None:
    import tkinter as tk

    root = tk.Tk()
    SubtractorApp(root)
    root.mainloop()


if __name__ == "__main__":
    main()
