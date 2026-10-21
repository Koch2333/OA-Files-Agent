# Document AI worker

Loopback, Bearer-token HTTP worker implementing the "Worker 协议" in `docs/backend-design.md`.

## Setup / run
    py -3.12 -m venv .venv
    .venv\Scripts\python.exe -m pip install -r requirements.txt
    set OA_WORKER_TOKEN=<32+ ascii chars>
    set OA_SPOOL_ROOT=<absolute spool dir>
    .venv\Scripts\python.exe server.py          (optional: OA_WORKER_PORT, OA_EXTRACT_TIMEOUT seconds, default 60)

Tests: `.venv\Scripts\python.exe -m unittest -v test_server` (about 1 minute; OCR models load per child process).
Fixtures: `.venv\Scripts\python.exe make_samples.py` regenerates `tests/samples/` and `manifest.json`.

## Design
- `server.py`: auth, path/size checks (realpath inside `OA_SPOOL_ROOT`, 20 MiB cap), error mapping. Never logs text or paths.
- `extract_core.py`: runs in a child process per request (`python -I`), killed on timeout (504); crash or garbage output gives 422 UNREADABLE_FILE. `/healthz` capabilities come from a cached import probe in a child.
- Formats: docx (python-docx, body order incl. tables, headers/footers), xlsx (openpyxl read_only, 5000 rows/sheet cap), PDF (PyMuPDF text per page; pages with < 20 native chars are rendered at 200 dpi and OCRed, max 50 OCR pages), png/jpg/jpeg (OCR). Text cap 200000 chars.

## Deviation from PaddleOCR
OCR uses `rapidocr-onnxruntime` (PaddleOCR PP-OCR detection/recognition models exported to ONNX, run by onnxruntime), because PaddlePaddle itself is heavy and awkward on Windows. Chinese and English are supported by the bundled models. Native pages have `confidence` 1.0; OCR pages report the mean line confidence.

## Limits
Legacy .doc/.xls are UNSUPPORTED_FORMAT. Password-protected OOXML (OLE wrapper) maps to ENCRYPTED. Handwriting and low-quality scans are not tuned.
