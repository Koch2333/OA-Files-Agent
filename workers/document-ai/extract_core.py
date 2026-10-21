"""Format extractors. Runs inside a child process (see server.py); never imported for parsing by the server."""
import json
import os
import sys

TEXT_CAP = 200000
MAX_XLSX_ROWS = 5000
MAX_PDF_PAGES = 300
MAX_OCR_PAGES = 50
MIN_NATIVE_CHARS = 20
OCR_DPI = 200
OLE_MAGIC = b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"
IMAGE_EXTS = {".png", ".jpg", ".jpeg"}
SUPPORTED_EXTS = {".docx", ".xlsx", ".pdf"} | IMAGE_EXTS


class ExtractError(Exception):
    def __init__(self, code, message=""):
        super().__init__(message or code)
        self.code = code


_ocr_engine = None


def probe():
    """Import-probe only; does not load models."""
    result = {"extraction_available": True, "ocr_available": True}
    try:
        import docx, openpyxl, pymupdf  # noqa: F401
    except Exception:
        result["extraction_available"] = False
    try:
        import rapidocr_onnxruntime  # noqa: F401
    except Exception:
        result["ocr_available"] = False
    return result


def get_ocr():
    global _ocr_engine
    if _ocr_engine is None:
        try:
            from rapidocr_onnxruntime import RapidOCR
            _ocr_engine = RapidOCR()
        except Exception as exc:
            raise ExtractError("OCR_UNAVAILABLE", "OCR engine unavailable") from exc
    return _ocr_engine


def ocr_image_bytes(data):
    """OCR encoded image bytes -> (text, mean confidence)."""
    import cv2
    import numpy as np
    array = cv2.imdecode(np.frombuffer(data, np.uint8), cv2.IMREAD_COLOR)
    if array is None:
        raise ExtractError("UNREADABLE_FILE", "image cannot be decoded")
    engine = get_ocr()
    try:
        result, _ = engine(array)
    except Exception as exc:
        raise ExtractError("UNREADABLE_FILE", "OCR failed") from exc
    if not result:
        return "", 0.0
    lines = []
    confs = []
    # Group boxes into lines by vertical position (result is roughly top-to-bottom).
    for box, text, conf in result:
        lines.append((box[0][1], box[0][0], text))
        confs.append(float(conf))
    lines.sort(key=lambda t: (round(t[0] / 10), t[1]))
    return "\n".join(t[2] for t in lines), round(sum(confs) / len(confs), 4)


def _meta(title=None, author=None, created=None, modified=None):
    def iso(value):
        return value.isoformat() if hasattr(value, "isoformat") else (value or None)
    return {"title": title or None, "author": author or None, "created": iso(created), "modified": iso(modified)}


def extract_docx(path, ocr):
    import zipfile
    with open(path, "rb") as f:
        head = f.read(8)
    if head == OLE_MAGIC:
        raise ExtractError("ENCRYPTED", "password protected office file")
    if not zipfile.is_zipfile(path):
        raise ExtractError("UNREADABLE_FILE", "not a docx container")
    import docx
    from docx.table import Table
    from docx.text.paragraph import Paragraph
    try:
        document = docx.Document(path)
        parts = []
        for section in document.sections:
            for header in (section.header, section.first_page_header):
                text = "\n".join(p.text for p in header.paragraphs if p.text.strip())
                for table in header.tables:
                    text += "\n" + _table_text(table)
                if text.strip() and text.strip() not in parts:
                    parts.append(text.strip())
        for child in document.element.body.iterchildren():
            tag = child.tag.rsplit("}", 1)[-1]
            if tag == "p":
                text = Paragraph(child, document).text
                if text.strip():
                    parts.append(text)
            elif tag == "tbl":
                parts.append(_table_text(Table(child, document)))
        for section in document.sections:
            text = "\n".join(p.text for p in section.footer.paragraphs if p.text.strip())
            if text.strip() and text.strip() not in parts:
                parts.append(text.strip())
        core = document.core_properties
        meta = _meta(core.title, core.author, core.created, core.modified)
    except ExtractError:
        raise
    except Exception as exc:
        raise ExtractError("UNREADABLE_FILE", "docx cannot be parsed") from exc
    return {"pages": [{"page": 1, "text": "\n".join(parts), "source": "native", "confidence": 1.0}],
            "ocr_used": False, "warnings": [], "metadata": meta}


def _table_text(table):
    rows = []
    for row in table.rows:
        cells = []
        seen = set()
        for cell in row.cells:
            if id(cell._tc) in seen:
                continue
            seen.add(id(cell._tc))
            cells.append(cell.text.replace("\n", " ").strip())
        if any(cells):
            rows.append("\t".join(cells))
    return "\n".join(rows)


def extract_xlsx(path, ocr):
    import zipfile
    with open(path, "rb") as f:
        head = f.read(8)
    if head == OLE_MAGIC:
        raise ExtractError("ENCRYPTED", "password protected office file")
    if not zipfile.is_zipfile(path):
        raise ExtractError("UNREADABLE_FILE", "not an xlsx container")
    import openpyxl
    warnings = []
    pages = []
    try:
        wb = openpyxl.load_workbook(path, read_only=True, data_only=True)
        try:
            for index, ws in enumerate(wb.worksheets, 1):
                lines = ["[" + ws.title + "]"]
                count = 0
                for row in ws.iter_rows(values_only=True):
                    count += 1
                    if count > MAX_XLSX_ROWS:
                        warnings.append("ROW_CAP_REACHED:" + ws.title)
                        break
                    cells = ["" if v is None else _cell(v) for v in row]
                    if any(c.strip() for c in cells):
                        lines.append("\t".join(cells).rstrip("\t"))
                pages.append({"page": index, "text": "\n".join(lines), "source": "native", "confidence": 1.0})
            props = wb.properties
            meta = _meta(props.title, props.creator, props.created, props.modified)
        finally:
            wb.close()
    except ExtractError:
        raise
    except Exception as exc:
        raise ExtractError("UNREADABLE_FILE", "xlsx cannot be parsed") from exc
    return {"pages": pages, "ocr_used": False, "warnings": warnings, "metadata": meta}


def _cell(value):
    if hasattr(value, "strftime"):
        return value.strftime("%Y-%m-%d") if getattr(value, "hour", 0) == 0 and getattr(value, "minute", 0) == 0 else value.isoformat(sep=" ")
    if isinstance(value, float) and value.is_integer():
        return str(int(value))
    return str(value)


def extract_pdf(path, ocr):
    import pymupdf as fitz
    try:
        doc = fitz.open(path)
    except Exception as exc:
        raise ExtractError("UNREADABLE_FILE", "pdf cannot be opened") from exc
    try:
        if doc.needs_pass or doc.is_encrypted:
            raise ExtractError("ENCRYPTED", "pdf is encrypted")
        warnings = []
        pages = []
        ocr_used = False
        total = doc.page_count
        if total == 0:
            raise ExtractError("UNREADABLE_FILE", "pdf has no pages")
        if total > MAX_PDF_PAGES:
            warnings.append("PAGE_CAP_REACHED")
        ocr_done = 0
        for number in range(min(total, MAX_PDF_PAGES)):
            try:
                page = doc[number]
                text = page.get_text("text") or ""
            except Exception as exc:
                raise ExtractError("UNREADABLE_FILE", "pdf page cannot be read") from exc
            if len(text.strip()) >= MIN_NATIVE_CHARS:
                pages.append({"page": number + 1, "text": text, "source": "native", "confidence": 1.0})
                continue
            if not ocr:
                pages.append({"page": number + 1, "text": text, "source": "native", "confidence": 1.0})
                if not text.strip():
                    warnings.append("OCR_DISABLED_EMPTY_PAGE:%d" % (number + 1))
                continue
            if ocr_done >= MAX_OCR_PAGES:
                warnings.append("OCR_PAGE_CAP_REACHED")
                pages.append({"page": number + 1, "text": text, "source": "native", "confidence": 1.0})
                continue
            ocr_done += 1
            try:
                pix = page.get_pixmap(dpi=OCR_DPI)
                data = pix.tobytes("png")
            except Exception as exc:
                raise ExtractError("UNREADABLE_FILE", "pdf page cannot be rendered") from exc
            ocr_text, conf = ocr_image_bytes(data)
            ocr_used = True
            pages.append({"page": number + 1, "text": (text.strip() + "\n" + ocr_text).strip(), "source": "ocr", "confidence": conf})
        meta = doc.metadata or {}
        return {"pages": pages, "ocr_used": ocr_used, "warnings": warnings,
                "metadata": _meta(meta.get("title"), meta.get("author"), meta.get("creationDate"), meta.get("modDate"))}
    finally:
        doc.close()


def extract_image(path, ocr):
    if not ocr:
        return {"pages": [{"page": 1, "text": "", "source": "native", "confidence": 1.0}], "ocr_used": False,
                "warnings": ["OCR_DISABLED_IMAGE"], "metadata": _meta()}
    with open(path, "rb") as f:
        data = f.read()
    text, conf = ocr_image_bytes(data)
    warnings = [] if text.strip() else ["OCR_NO_TEXT"]
    return {"pages": [{"page": 1, "text": text, "source": "ocr", "confidence": conf}], "ocr_used": True,
            "warnings": warnings, "metadata": _meta()}


def extract(path, ext, ocr):
    if ext == ".docx":
        result = extract_docx(path, ocr)
    elif ext == ".xlsx":
        result = extract_xlsx(path, ocr)
    elif ext == ".pdf":
        result = extract_pdf(path, ocr)
    elif ext in IMAGE_EXTS:
        result = extract_image(path, ocr)
    else:
        raise ExtractError("UNSUPPORTED_FORMAT", "unsupported format")
    full = "\n\n".join(p["text"] for p in result["pages"] if p["text"])
    result["truncated"] = len(full) > TEXT_CAP
    result["text"] = full[:TEXT_CAP]
    if result["truncated"]:
        for p in result["pages"]:
            p["text"] = p["text"][:TEXT_CAP]
        result["warnings"].append("TEXT_TRUNCATED")
    return result


def main():
    # argv: path ext ocr(0/1). Result JSON goes to stdout as one UTF-8 line.
    out = sys.stdout.buffer
    if sys.argv[1] == "--probe":
        out.write(json.dumps(probe()).encode("utf-8") + b"\n")
        out.flush()
        return
    path, ext, ocr = sys.argv[1], sys.argv[2], sys.argv[3] == "1"
    try:
        body = {"data": extract(path, ext, ocr)}
    except ExtractError as exc:
        body = {"error": {"code": exc.code, "message": str(exc)}}
    except MemoryError:
        body = {"error": {"code": "UNREADABLE_FILE", "message": "out of memory"}}
    except Exception:
        body = {"error": {"code": "UNREADABLE_FILE", "message": "extraction failed"}}
    out.write(json.dumps(body, ensure_ascii=False).encode("utf-8") + b"\n")
    out.flush()


if __name__ == "__main__":
    main()
