"""Authenticated loopback Document AI worker. Extraction runs in a child process with a timeout."""
import hmac
import json
import os
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import formats_data  # generated from contracts/formats.json (scripts/sync_formats.py)

HERE = os.path.dirname(os.path.abspath(__file__))
CORE = os.path.join(HERE, "extract_core.py")
def _env_bytes(name, default):
    """Byte count from the environment; 0 means unlimited. Invalid values fall back to the default."""
    try:
        value = int(os.environ.get(name, ""))
    except ValueError:
        return default
    return value if value >= 0 else default


# Full-text extraction/OCR is skipped above this size (the file is still imported; the pipeline marks it unreadable-content /
# manual review, judged by file name only). This protects worker memory only and is NOT an import limit. 0 = no cap.
MAX_FILE_BYTES = _env_bytes("OA_EXTRACT_MAX_BYTES", 256 * 1024 * 1024)
MAX_BODY_BYTES = 64 * 1024
MAX_PAGES_FIELD = 50
PROFILES = ("default", "enhanced", "rotate", "deskew", "high_dpi", "binarize")
_FORMATS = formats_data.TABLE["formats"]
SUPPORTED_EXTS = {f["ext"] for f in _FORMATS}
RENDER_EXTS = {f["ext"] for f in _FORMATS if f["group"] in ("image", "pdf")}
DEFAULT_TIMEOUT = 60.0
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


CHILD_MODES = ("extract", "render", "preview")


def child_command(mode="extract", frozen=None, executable=None):
    """Command prefix for the isolated child. Frozen (PyInstaller): the executable re-invokes itself with
    --<mode>-child (see worker_main.py); otherwise extract_core.py runs under `python -I` (render/preview take a
    --render/--preview flag)."""
    if mode not in CHILD_MODES:
        raise ValueError("unknown child mode")
    frozen = getattr(sys, "frozen", False) if frozen is None else frozen
    executable = executable or sys.executable
    if frozen:
        return [executable, "--%s-child" % mode]
    cmd = [executable, "-I", CORE]
    return cmd + ([] if mode == "extract" else ["--" + mode])


def _run_child(args, timeout, child_cmd=None, mode="extract"):
    """Run the extractor child; returns parsed JSON or raises ApiError. Output never logged."""
    cmd = list(child_cmd) if child_cmd else child_command(mode)
    # A child that dies or prints garbage may be a transient failure (memory pressure, a killed process), so it is
    # retried once; a genuinely corrupt file fails the same way twice and is reported as unreadable.
    # Timeouts are never retried.
    for attempt in (1, 2):
        proc = subprocess.Popen(cmd + args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                stderr=subprocess.DEVNULL, cwd=HERE)
        try:
            out, _ = proc.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.communicate()
            raise ApiError("EXTRACT_TIMEOUT", "extraction timed out")
        if proc.returncode == 0:
            try:
                return json.loads(out.decode("utf-8"))
            except (ValueError, UnicodeDecodeError):
                pass
        if attempt == 2:
            raise ApiError("UNREADABLE_FILE", "extraction process failed")


def parse_roots(raw):
    """OA_ALLOWED_ROOTS style string (os.pathsep separated) -> list of absolute roots."""
    return [p for p in (raw or "").split(os.pathsep) if p.strip() and os.path.isabs(p.strip())]


def resolve_path(raw, spool_root, extra_roots=()):
    """realpath must be inside the spool root or (for callers that pass them) one of extra_roots."""
    roots = [r for r in [spool_root, *extra_roots] if r]
    if not roots:
        raise ApiError("PATH_NOT_ALLOWED", "path outside spool root")
    if not isinstance(raw, str) or not raw or "\x00" in raw or not os.path.isabs(raw):
        raise ApiError("INVALID_REQUEST", "path must be an absolute path")
    real = os.path.realpath(raw)
    inside = False
    for candidate in roots:
        root = os.path.realpath(candidate)
        try:
            ok = os.path.commonpath([os.path.normcase(root), os.path.normcase(real)]) == os.path.normcase(root)
        except ValueError:
            ok = False
        if ok and os.path.normcase(real) != os.path.normcase(root):
            inside = True
            break
    if not inside:
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
        routes = {"/v1/extract": self.extract, "/v1/render": self.render, "/v1/preview": self.preview}
        handler = routes.get(self.path)
        if handler is None:
            self.reply(404, {"error": {"code": "NOT_FOUND", "message": "not found"}})
            return
        try:
            self.reply(200, {"data": handler()})
        except ApiError as exc:
            self.reply(STATUS.get(exc.code, 500), {"error": {"code": exc.code, "message": exc.message}})
        except Exception:
            self.reply(500, {"error": {"code": "INTERNAL", "message": "internal error"}})

    def read_request(self):
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
        return req

    def run(self, mode, args):
        with self.server.slots:
            body = _run_child(args, self.server.timeout, self.server.child_cmd, mode)
        if "error" in body:
            err = body["error"]
            raise ApiError(err.get("code", "UNREADABLE_FILE"), err.get("message", ""))
        return body["data"]

    def extract(self):
        req = self.read_request()
        ocr = req.get("ocr", True)
        filename = req.get("filename", "")
        profile = req.get("ocr_profile")
        profile = "default" if profile is None else profile
        pages = req.get("pages")
        if not isinstance(ocr, bool) or not isinstance(filename, str):
            raise ApiError("INVALID_REQUEST", "invalid field type")
        if not isinstance(profile, str) or profile not in PROFILES:
            raise ApiError("INVALID_REQUEST", "unknown ocr_profile")
        if pages is not None:
            if (not isinstance(pages, list) or len(pages) > MAX_PAGES_FIELD
                    or any(isinstance(p, bool) or not isinstance(p, int) or p < 1 for p in pages)):
                raise ApiError("INVALID_REQUEST", "pages must be up to 50 integers >= 1")
            pages = sorted(set(pages))
        real = resolve_path(req.get("path"), self.server.spool_root)  # extraction stays spool-only
        if MAX_FILE_BYTES and os.path.getsize(real) > MAX_FILE_BYTES:
            raise ApiError("FILE_TOO_LARGE", "file too large")
        # The extension is only a claim: the extractor sniffs the content (unknown extensions are still looked at, but
        # only recognised binary formats are accepted there).
        ext = os.path.splitext(filename)[1].lower() or os.path.splitext(real)[1].lower()
        args = [real, ext, "1" if ocr else "0", profile, ",".join(str(p) for p in pages or [])]
        return self.run("extract", args)

    def _checked_file(self, req):
        real = resolve_path(req.get("path"), self.server.spool_root, self.server.extra_roots)
        if MAX_FILE_BYTES and os.path.getsize(real) > MAX_FILE_BYTES:
            raise ApiError("FILE_TOO_LARGE", "file too large")
        return real, os.path.splitext(real)[1].lower()

    @staticmethod
    def _int_field(req, name, default, lo, hi):
        value = req.get(name, default)
        if isinstance(value, bool) or not isinstance(value, int) or not lo <= value <= hi:
            raise ApiError("INVALID_REQUEST", "invalid " + name)
        return value

    def render(self):
        req = self.read_request()
        page = self._int_field(req, "page", 1, 1, 100000)
        max_side = self._int_field(req, "max_side", 1400, 256, 2000)
        real, ext = self._checked_file(req)
        if ext not in RENDER_EXTS:
            raise ApiError("UNSUPPORTED_FORMAT", "format cannot be rendered")
        return self.run("render", [real, ext, str(page), str(max_side)])

    def preview(self):
        req = self.read_request()
        max_rows = self._int_field(req, "max_rows", 200, 1, 2000)
        max_blocks = self._int_field(req, "max_blocks", 600, 1, 5000)
        real, ext = self._checked_file(req)
        return self.run("preview", [real, ext, str(max_rows), str(max_blocks)])

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


def create_server(port=8090, token=None, spool_root=None, timeout=None, child_cmd=None, max_parallel=2,
                  extra_roots=None):
    token = token or os.environ.get("OA_WORKER_TOKEN", "")
    if len(token) < 32 or not token.isascii():
        raise ValueError("OA_WORKER_TOKEN must contain at least 32 ASCII characters")
    server = WorkerServer(("127.0.0.1", port), Handler)
    server.worker_token = token
    server.spool_root = spool_root if spool_root is not None else os.environ.get("OA_SPOOL_ROOT", "")
    server.timeout = float(timeout if timeout is not None else os.environ.get("OA_EXTRACT_TIMEOUT", DEFAULT_TIMEOUT))
    server.child_cmd = child_cmd
    # extra roots apply to /v1/preview and /v1/render only; /v1/extract stays restricted to the spool root
    server.extra_roots = parse_roots(os.environ.get("OA_ALLOWED_ROOTS", "")) if extra_roots is None else list(extra_roots)
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
