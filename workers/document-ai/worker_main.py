"""PyInstaller entry point: `document-worker.exe` serves HTTP; `document-worker.exe --extract-child ...` is the isolated
extractor, `--render-child` the page renderer and `--preview-child` the structured previewer."""
import sys

CHILD_MODES = {"--extract-child": None, "--render-child": "--render", "--preview-child": "--preview"}

if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] in CHILD_MODES:
        mode = CHILD_MODES[sys.argv[1]]
        sys.argv = [sys.argv[0]] + ([mode] if mode else []) + sys.argv[2:]
        import extract_core
        extract_core.main()
    else:
        import server
        server.main()
