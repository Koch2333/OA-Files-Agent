"""PyInstaller entry point: `document-worker.exe` serves HTTP; `document-worker.exe --extract-child ...` is the isolated extractor."""
import sys

if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--extract-child":
        sys.argv = [sys.argv[0]] + sys.argv[2:]
        import extract_core
        extract_core.main()
    else:
        import server
        server.main()
