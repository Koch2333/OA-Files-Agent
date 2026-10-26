# Document AI worker

Loopback, Bearer-token HTTP worker implementing the "Worker 协议" in `docs/backend-design.md`.

## Setup / run
    py -3.12 -m venv .venv
    .venv\Scripts\python.exe -m pip install -r requirements.txt
    set OA_WORKER_TOKEN=<32+ ascii chars>
    set OA_SPOOL_ROOT=<absolute spool dir>
    .venv\Scripts\python.exe server.py          (optional: OA_WORKER_PORT, OA_EXTRACT_TIMEOUT seconds, default 60)

Tests: `.venv\Scripts\python.exe -m unittest -v test_server` (about 1 minute; OCR models load per child process).
Fixtures: `.venv\Scripts\python.exe make_samples.py` regenerates `tests/samples/` and `manifest.json`; `make_format_samples.py` writes the script-built files of `tests/samples/formats/` and `formats-manifest.json`; `make_office_fixtures.ps1` (Windows + Office, COM) regenerates the genuine Office-produced ones.

## Design
- `server.py`: auth, path/size checks (realpath inside `OA_SPOOL_ROOT`, 20 MiB cap), error mapping. Never logs text or paths.
- `extract_core.py`: runs in a child process per request (`python -I`), killed on timeout (504); crash or garbage output gives 422 UNREADABLE_FILE. `/healthz` capabilities come from a cached import probe in a child.
- Formats (single table: `contracts/formats.json` -> generated `formats_data.py`, see `scripts/sync_formats.py`): docx (python-docx, body order incl. tables, headers/footers), xlsx (openpyxl read_only, 5000 rows/sheet cap), PDF (PyMuPDF text per page; pages with < 20 native chars are rendered at 200 dpi and OCRed, max 50 OCR pages), images png/jpg/jpeg/jfif/bmp/tif(f, multi-page)/webp/gif (OCR; Pillow decodes the formats OpenCV cannot). Text cap 200000 chars.
- Additional readers (all read-only, no macros/links/objects executed, size/expansion/time caps, structured failures):
  - `ole_formats.py`: `.doc` (hand-written: FIB + piece table + PAPX table flags, compressed and UTF-16 pieces, header/footer/footnote/textbox stories, fallback scan -> `partial`), `.ppt` (hand-written: record tree + persist directory, slide text, notes), `.xls` (xlrd >= 2: cached formula values, dates, hidden sheets), `.msg` (properties via olefile). OLE access goes through olefile with a bounded wrapper (declared stream sizes are not trusted, cumulative read cap); the SummaryInformation parser is our own (olefile's `get_metadata` can spin on hostile input).
  - `zip_formats.py`: OpenDocument (.odt/.ods/.odp) and .pptx with the stdlib only; zip bomb limits; DTD/entity declarations are refused before parsing.
  - `text_formats.py`: txt/md/csv/tsv with encoding detection (BOM, UTF-16, strict UTF-8, GB18030, Big5, then cp1252 flagged `LOW_CONFIDENCE_TEXT`), HTML (visible text only), RTF (own reader), .eml (stdlib email).
  - `sniff.py`: the extension is only a claim. Magic bytes / container members decide the reader; a mismatch is reported (`detected_format`, `extension_mismatch`, warning `FORMAT_MISMATCH:<fmt>`) and the real format is read (e.g. an HTML table saved as `.xls`). Unknown binary -> `UNSUPPORTED_FORMAT`; Office names with unrecognisable bytes -> `UNREADABLE_FILE`; archives are not opened.
  - `.wps/.et/.dps` are read only when their content is Office-compatible (OLE .doc/.xls/.ppt or OOXML/ODF); native Kingsoft files are `UNSUPPORTED_FORMAT`.
- Extract result additions: `content_status` (`ok|partial|unreadable`), `content_reason` (`capped|damaged|encoding|ocr_empty`), `detected_format`, `extension_mismatch`. Files that cannot be read at all stay structured errors (HTTP 422/413/504 with `ENCRYPTED`, `UNREADABLE_FILE`, `UNSUPPORTED_FORMAT`, `FILE_TOO_LARGE`, `EXTRACT_TIMEOUT`); the Go pipeline turns every one of them (and `ocr_empty`) into a filename-only analysis instead of a failed file.

## Deviation from PaddleOCR
OCR uses `rapidocr-onnxruntime` (PaddleOCR PP-OCR detection/recognition models exported to ONNX, run by onnxruntime), because PaddlePaddle itself is heavy and awkward on Windows. Chinese and English are supported by the bundled models. Native pages have `confidence` 1.0; OCR pages report the mean line confidence.

## Limits
Password-protected Office files (OOXML OLE wrapper, .doc/.xls/.ppt encryption, ODF encryption) map to ENCRYPTED. Word 6.0/95 files are UNSUPPORTED_FORMAT. Legacy .doc/.ppt readers are validated against genuine Microsoft Office 16 output only (see `tests/samples/formats/formats-manifest.json`); real-world files from other producers may be read partially (`partial`). Handwriting and low-quality scans are not tuned.

## OCR rescue / render / preview (see `docs/ocr-rescue-design.md`)
- `POST /v1/extract` accepts optional `ocr_profile` (`default|enhanced|rotate|deskew|high_dpi|binarize`, unknown -> 400) and `pages` (<= 50 integers >= 1; out of range -> 400; only those pages are OCRed). The response has `quality` (null when no OCR ran): mean_confidence, low_confidence_ratio (line confidence < 0.6), chars, cjk_ratio, noise_ratio, lines, profile, rotation (degrees clockwise applied), weak_pages, low_confidence_lines (<= 10, text <= 80 chars). Non-default profiles cap images at 25 megapixels (PDF dpi is lowered accordingly).
- `POST /v1/render` `{path, page, max_side 256-2000}` -> PNG (base64, <= 2.5 MiB base64; dimensions shrink, never JPEG). `POST /v1/preview` `{path, max_rows, max_blocks}` -> structured docx/xlsx blocks or pdf/image kind.
- All three run in an isolated child with the extraction timeout. Frozen builds re-invoke the exe with `--extract-child`, `--render-child` or `--preview-child` (`server.child_command`, `worker_main.py`).
- Paths: `/v1/extract` accepts only `OA_SPOOL_ROOT`; `/v1/preview` and `/v1/render` also accept `OA_ALLOWED_ROOTS` (separated by `os.pathsep`). Realpath/symlink checks apply to every root.

### Profile evaluation (measured on this machine)
`make_degraded.py` writes 44 degraded variants of 4 OCR samples (2 photos, 2 scanned PDFs) to `tests/samples/degraded/` with `manifest.json`; `eval_profiles.py` runs every profile and reports keyword recall (share of the sample's manifest keywords found). 4 files per row; one run, rapidocr-onnxruntime 1.4.4.

| degradation | default | enhanced | rotate | deskew | high_dpi | binarize |
|---|---|---|---|---|---|---|
| rot90 | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 |
| rot180 | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 |
| skew8 | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 |
| low_contrast | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 |
| noise_blur | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 |
| small_font (0.35x, JPEG q45) | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 | 0.50 |
| low_contrast_extreme | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 | 0.50 |
| noise_heavy | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 | 0.88 |
| small_font_tiny (0.2x, JPEG q30) | 0.75 | 0.50 | 0.75 | 0.75 | 0.75 | 0.00 |
| rot90_noise | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 | 0.88 |
| skew14_noise | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 |
| mean seconds/file (all runs) | ~4 | ~5-6 | ~15-17 | ~3 | ~4 | ~3.5 |

Honest reading: on these synthetic degradations **no profile improved on `default`**. The bundled rapidocr models already read pages rotated by 90/180 degrees and skewed up to 14 degrees, so `rotate` and `deskew` never changed the outcome (`rotate` always kept rotation 0 because no other orientation scored 1.25x better) and cost up to 4x the time. `enhanced` and `binarize` made the hardest case (tiny font) worse, and `binarize` hurt low-contrast and noisy inputs. `high_dpi` is neutral for images (2x upscale did not help). The sample set is small and synthetic (clean renders degraded in software), so these results do not show the profiles are useless on real camera photos; they only mean there is no evidence here that any of them beat `default`, and callers should compare quality before and after a retry (as the design's `rerun_ocr` does) rather than assume improvement. The unit test `RotationRescueTest` therefore asserts that `rotate` is not worse than `default` and recovers >= 50% of keywords on the 90/180 samples, not that it is strictly better.
