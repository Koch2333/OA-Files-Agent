"""Authenticated loopback Document AI worker. Extraction runs in a child process with a timeout."""
import hmac
import json
import os
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
CORE = os.path.join(HERE, "extract_core.py")
MAX_FILE_BYTES = 20 * 1024 * 1024
MAX_BODY_BYTES = 64 * 1024
DEFAULT_TIMEOUT = 60.0
SUPPORTED_EXTS = {".docx", ".xlsx", ".pdf", ".png", ".jpg", ".jpeg"}
LEGACY_EXTS = {".doc", ".xls", ".ppt", ".pptx"}
STATUS = {
    "INVALID_REQUEST": 400, "PATH_NOT_ALLOWED": 403, "FILE_NOT_FOUND": 404, "FILE_TOO_LARGE": 413,
    "UNREADABLE_FILE": 422, "ENCRYPTED": 422, "UNSUPPORTED_FORMAT": 422,
    "OCR_UNAVAILABLE": 503, "EXTRACT_TIMEOUT": 504,
}


class ApiError(Exception):
    def __init__(self, code, message=""):
        super().__init__(code)
        self.code = code
        self.message = message or code


def _run_child(args, timeout, child_cmd=None):
    """Run the extractor child; returns parsed JSON or raises ApiError. Output never logged."""
    if child_cmd:
        cmd = list(child_cmd)
    elif getattr(sys, "frozen", False):  # PyInstaller build: the executable re-invokes itself as the extractor child
        cmd = [sys.executable, "--extract-child"]
    else:
        cmd = [sys.executable, "-I", CORE]
    proc = subprocess.Popen(cmd + args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, cwd=HERE)
    try:
        out, _ = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.communicate()
        raise ApiError("EXTRACT_TIMEOUT", "extraction timed out")
    if proc.returncode != 0:
        raise ApiError("UNREADABLE_FILE", "extraction process failed")
    try:
        return json.loads(out.decode("utf-8"))
    except (ValueError, UnicodeDecodeError):
        raise ApiError("UNREADABLE_FILE", "extraction produced no result")


def resolve_path(raw, spool_root):
    if not spool_root:
        raise ApiError("PATH_NOT_ALLOWED", "path outside spool root")
    if not isinstance(raw, str) or not raw or "\x00" in raw or not os.path.isabs(raw):
        raise ApiError("INVALID_REQUEST", "path must be an absolute path")
    root = os.path.realpath(spool_root)
    real = os.path.realpath(raw)
    try:
        inside = os.path.commonpath([os.path.normcase(root), os.path.normcase(real)]) == os.path.normcase(root)
    except ValueError:
        inside = False
    if not inside or os.path.normcase(real) == os.path.normcase(root):
        raise ApiError("PATH_NOT_ALLOWED", "path outside spool root")
    if not os.path.isfile(real):
        raise ApiError("FILE_NOT_FOUND", "file not found")
    return real


class Handler(BaseHTTPRequestHandler):
    def authorized(self):
        supplied = self.headers.get("Authorization", "")
        if hmac.compare_digest(supplied.encode("utf-8"), ("Bearer " + self.server.worker_token).encode("utf-8")):
            return True
        self.reply(401, {"error": {"code": "UNAUTHORIZED", "message": "unauthorized"}})
        return False

    def do_GET(self):
        if not self.authorized():
            return
        if self.path == "/healthz":
            caps = self.server.capabilities()
            self.reply(200, {"data": {"status": "ok", "extraction_available": caps["extraction_available"],
                                      "ocr_available": caps["ocr_available"]}})
        else:
            self.reply(404, {"error": {"code": "NOT_FOUND", "message": "not found"}})

    def do_POST(self):
        if not self.authorized():
            return
        if self.path != "/v1/extract":
            self.reply(404, {"error": {"code": "NOT_FOUND", "message": "not found"}})
            return
        try:
            self.reply(200, {"data": self.extract()})
        except ApiError as exc:
            self.reply(STATUS.get(exc.code, 500), {"error": {"code": exc.code, "message": exc.message}})
        except Exception:
            self.reply(500, {"error": {"code": "INTERNAL", "message": "internal error"}})

    def extract(self):
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            length = -1
        if length <= 0 or length > MAX_BODY_BYTES:
            raise ApiError("INVALID_REQUEST", "invalid body size")
        try:
            req = json.loads(self.rfile.read(length).decode("utf-8"))
        except (ValueError, UnicodeDecodeError):
            raise ApiError("INVALID_REQUEST", "body must be JSON")
        if not isinstance(req, dict):
            raise ApiError("INVALID_REQUEST", "body must be an object")
        ocr = req.get("ocr", True)
        filename = req.get("filename", "")
        if not isinstance(ocr, bool) or not isinstance(filename, str):
            raise ApiError("INVALID_REQUEST", "invalid field type")
        real = resolve_path(req.get("path"), self.server.spool_root)
        if os.path.getsize(real) > MAX_FILE_BYTES:
            raise ApiError("FILE_TOO_LARGE", "file too large")
        ext = os.path.splitext(filename)[1].lower() or os.path.splitext(real)[1].lower()
        if ext not in SUPPORTED_EXTS:
            raise ApiError("UNSUPPORTED_FORMAT", "unsupported format")
        with self.server.slots:
            body = _run_child([real, ext, "1" if ocr else "0"], self.server.timeout, self.server.child_cmd)
        if "error" in body:
            err = body["error"]
            raise ApiError(err.get("code", "UNREADABLE_FILE"), err.get("message", ""))
        return body["data"]

    def reply(self, status, body):
        payload = json.dumps(body, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *_):
        pass  # Do not log document paths or authentication headers.


class WorkerServer(ThreadingHTTPServer):
    daemon_threads = True

    def capabilities(self):
        with self._cap_lock:
            if self._caps is None:
                try:
                    self._caps = _run_child(["--probe"], 120)
                except ApiError:
                    self._caps = {"extraction_available": False, "ocr_available": False}
            return self._caps


def create_server(port=8090, token=None, spool_root=None, timeout=None, child_cmd=None, max_parallel=2):
    token = token or os.environ.get("OA_WORKER_TOKEN", "")
    if len(token) < 32 or not token.isascii():
        raise ValueError("OA_WORKER_TOKEN must contain at least 32 ASCII characters")
    server = WorkerServer(("127.0.0.1", port), Handler)
    server.worker_token = token
    server.spool_root = spool_root if spool_root is not None else os.environ.get("OA_SPOOL_ROOT", "")
    server.timeout = float(timeout if timeout is not None else os.environ.get("OA_EXTRACT_TIMEOUT", DEFAULT_TIMEOUT))
    server.child_cmd = child_cmd
    server.slots = threading.Semaphore(max_parallel)
    server._caps = None
    server._cap_lock = threading.Lock()
    return server


def main():
    _server = create_server(int(os.environ.get("OA_WORKER_PORT", "8090")))
    # Warm the capability probe (imports the OCR models) so the first /healthz answers quickly.
    threading.Thread(target=_server.capabilities, daemon=True).start()
    _server.serve_forever()


if __name__ == "__main__":
    main()
