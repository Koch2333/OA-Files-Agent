"""Run real PostgreSQL integration tests on Windows without installing a service.

Development-only binaries: https://github.com/zonkyio/embedded-postgres-binaries
Downloaded from Maven Central, not part of the application distribution.
"""
import hashlib
import io
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
TOOLS = ROOT / ".tools"
VERSION = "17.6.0"
NAME = f"embedded-postgres-binaries-windows-amd64-{VERSION}.jar"
URL = f"https://repo.maven.apache.org/maven2/io/zonky/test/postgres/embedded-postgres-binaries-windows-amd64/{VERSION}/{NAME}"


def run(args, **kwargs):
    return subprocess.run(args, check=True, timeout=120, creationflags=subprocess.CREATE_NO_WINDOW, **kwargs)


def binaries():
    destination = TOOLS / f"postgres-{VERSION}"
    if (destination / "bin/pg_ctl.exe").exists():
        return destination / "bin"
    TOOLS.mkdir(exist_ok=True)
    archive = TOOLS / NAME
    if not archive.exists():
        with urllib.request.urlopen(URL, timeout=60) as response:
            payload = response.read(64 * 1024 * 1024)
        # Maven's published SHA-1 is a transport-integrity check, not a signature.
        with urllib.request.urlopen(URL + ".sha1", timeout=30) as response:
            expected = response.read().decode().strip().split()[0]
        if hashlib.sha1(payload).hexdigest() != expected:
            raise ValueError("PostgreSQL archive checksum mismatch")
        archive.write_bytes(payload)
        archive.with_suffix(".sha256").write_text(hashlib.sha256(payload).hexdigest(), encoding="ascii")
    with zipfile.ZipFile(archive) as jar:
        members = [name for name in jar.namelist() if name.endswith((".txz", ".tar.xz", ".tgz"))]
        if len(members) != 1:
            raise ValueError(f"Unexpected PostgreSQL bundle: {jar.namelist()}")
        destination.mkdir(exist_ok=True)
        with tarfile.open(fileobj=io.BytesIO(jar.read(members[0])), mode="r:*") as tar:
            tar.extractall(destination, filter="data")
    if not (destination / "bin/pg_ctl.exe").exists():
        raise ValueError("Missing pg_ctl.exe in extracted bundle")
    return destination / "bin"


def main():
    if sys.platform != "win32":
        raise ValueError("This bootstrap is Windows-only; use a PostgreSQL service on Linux CI")
    binary = binaries()
    run_root = Path(tempfile.mkdtemp(prefix="pg-test-", dir=TOOLS))
    cluster = run_root / "data"
    password_file = run_root / "password"
    password = secrets.token_hex(24)
    password_file.write_text(password, encoding="ascii")
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    try:
        run([str(binary / "initdb.exe"), "-D", str(cluster), "-U", "oa_test", "-A", "scram-sha-256", "--pwfile", str(password_file), "--encoding=UTF8", "--locale=C"], capture_output=True)
    finally:
        password_file.unlink(missing_ok=True)
    started = False
    try:
        # A detached Windows server can inherit pipes; use files, not PIPE/communicate.
        with (run_root / "control.log").open("wb") as control:
            run([str(binary / "pg_ctl.exe"), "-D", str(cluster), "-l", str(run_root / "postgres.log"), "-o", f"-h 127.0.0.1 -p {port}", "-w", "start"], stdout=control, stderr=control)
        started = True
        env = dict(os.environ, OA_TEST_DATABASE_URL=f"postgres://oa_test:{password}@127.0.0.1:{port}/postgres?sslmode=disable")
        packages = sys.argv[1:] or ["./internal/db"]
        run(["go", "test", *packages, "-v", "-count=1"], cwd=ROOT / "backend", env=env)
    finally:
        if started:
            with (run_root / "control.log").open("ab") as control:
                run([str(binary / "pg_ctl.exe"), "-D", str(cluster), "-m", "fast", "-w", "stop"], stdout=control, stderr=control)
        print(f"Test database stopped; local artifacts: {run_root.relative_to(ROOT)}")


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        # initdb/pg_ctl do not print the password passed through the temporary file.
        print((error.stderr or error.stdout or b"").decode(errors="replace") if isinstance(error.stderr or error.stdout, bytes) else str(error), file=sys.stderr)
        sys.exit(1)
