"""Shared pieces for the additional format readers: error type, the normalised intermediate form ("parsed") every
reader returns, the safe-XML helper and converters to the extract / preview responses.

parsed = {
  "family": "document" | "spreadsheet" | "presentation",
  "blocks": [block, ...]                      # document: heading / paragraph / list_item / table blocks
  "slides": [{"blocks": [...], "notes": str, "hidden": bool}]   # presentation
  "sheets": [{"name", "rows", "hidden", "rows_total", "cols_total"}]  # spreadsheet
  "header": str, "footer": str, "metadata": {...}, "warnings": [str], "partial": bool, "low_confidence": bool,
}
Nothing here executes or fetches anything found inside a document.
"""
import re


class ExtractError(Exception):
    def __init__(self, code, message=""):
        super().__init__(message or code)
        self.code = code


TEXT_CAP = 200000
MAX_ROWS = 5000          # per sheet, extraction
PREVIEW_COLS = 50
PREVIEW_CELL = 200
MAX_COLS = 200           # columns kept per row when extracting
MAX_BLOCKS = 200000
MAX_UNCOMPRESSED = 256 * 1024 * 1024   # total bytes a zip/OLE container may expand to
MAX_PART_BYTES = 128 * 1024 * 1024     # one XML part / OLE stream

_CTRL = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")


def clean(text):
    """Drop control characters (keeps tab, newline, CR)."""
    return _CTRL.sub("", text) if text else ""


def clip(text, limit=PREVIEW_CELL):
    text = "" if text is None else str(text)
    return text if len(text) <= limit else text[:limit]


def meta(title=None, author=None, created=None, modified=None):
    def iso(value):
        return value.isoformat() if hasattr(value, "isoformat") else (value or None)
    def text(value):
        return (clean(value).strip() or None) if isinstance(value, str) else None
    return {"title": text(title), "author": text(author), "created": iso(created), "modified": iso(modified)}


def new_parsed(family):
    return {"family": family, "blocks": [], "slides": [], "sheets": [], "header": "", "footer": "", "metadata": meta(),
            "warnings": [], "partial": False, "low_confidence": False}


def paragraph(text, align="left"):
    return {"type": "paragraph", "runs": [{"text": text, "bold": False, "italic": False}], "align": align}


def heading(text, level=1):
    return {"type": "heading", "level": max(1, min(6, level)), "text": text}


def list_item(text, level=0):
    return {"type": "list_item", "level": level, "runs": [{"text": text, "bold": False, "italic": False}]}


def table(rows):
    return {"type": "table", "rows": rows}


def _block_text(block):
    kind = block["type"]
    if kind == "heading":
        return block["text"]
    if kind in ("paragraph", "list_item"):
        return "".join(r["text"] for r in block["runs"])
    if kind == "table":
        return "\n".join("\t".join(c for c in row).rstrip("\t") for row in block["rows"])
    return ""


def blocks_text(blocks):
    return "\n".join(t for t in (_block_text(b) for b in blocks) if t.strip())


def cell_text(value):
    """Scalar -> display string (same rules as the xlsx path)."""
    if value is None:
        return ""
    if hasattr(value, "strftime"):
        if getattr(value, "hour", 0) == 0 and getattr(value, "minute", 0) == 0 and getattr(value, "second", 0) == 0:
            return value.strftime("%Y-%m-%d")
        return value.isoformat(sep=" ")
    if isinstance(value, bool):
        return "TRUE" if value else "FALSE"
    if isinstance(value, float) and value.is_integer() and abs(value) < 1e15:
        return str(int(value))
    return str(value)


def to_extract(parsed):
    """-> {"pages": [...], "warnings": [...], "metadata": {...}} (the extract_core result shape, without OCR)."""
    pages = []
    family = parsed["family"]
    if family == "spreadsheet":
        for index, sheet in enumerate(parsed["sheets"], 1):
            lines = ["[" + sheet["name"] + (" (隐藏)" if sheet.get("hidden") else "") + "]"]
            for row in sheet["rows"]:
                if any(c.strip() for c in row):
                    lines.append("\t".join(row).rstrip("\t"))
            pages.append({"page": index, "text": "\n".join(lines), "source": "native", "confidence": 1.0})
    elif family == "presentation":
        for index, slide in enumerate(parsed["slides"], 1):
            text = blocks_text(slide["blocks"])
            if slide.get("notes"):
                text += ("\n" if text else "") + "[备注] " + slide["notes"]
            pages.append({"page": index, "text": text, "source": "native", "confidence": 1.0})
        if not pages:
            pages.append({"page": 1, "text": "", "source": "native", "confidence": 1.0})
    else:
        parts = []
        if parsed["header"].strip():
            parts.append(parsed["header"].strip())
        body = blocks_text(parsed["blocks"])
        if body:
            parts.append(body)
        if parsed["footer"].strip():
            parts.append(parsed["footer"].strip())
        pages.append({"page": 1, "text": "\n".join(parts), "source": "native", "confidence": 1.0})
    warnings = list(parsed["warnings"])
    if parsed["partial"]:
        warnings.append("LEGACY_PARTIAL")
    if parsed["low_confidence"]:
        warnings.append("LOW_CONFIDENCE_TEXT")
    return {"pages": pages, "ocr_used": False, "warnings": warnings, "metadata": parsed["metadata"]}


def to_preview(parsed, max_rows, max_blocks):
    """-> the /v1/preview structured response ("document" or "spreadsheet")."""
    friendly = {"DOC_TABLE_STRUCTURE_APPROXIMATE": "表格结构为近似还原", "TEXT_TRUNCATED": "内容过长，仅显示前部"}
    warnings = [friendly[w] for w in parsed["warnings"] if w in friendly]
    warnings += [w for w in parsed["warnings"] if not re.match(r"^[A-Z0-9_]+(:|$)", w)]
    if parsed["partial"]:
        warnings.append("部分内容可能无法完整还原，仅供参考")
    if parsed["low_confidence"]:
        warnings.append("文本编码为推测结果，可能存在乱码")
    if parsed["family"] == "spreadsheet":
        sheets = []
        for sheet in parsed["sheets"]:
            if sheet.get("hidden"):
                continue
            rows = [[clip(c) for c in row[:PREVIEW_COLS]] for row in sheet["rows"][:max_rows]]
            rows_total = max(sheet.get("rows_total") or 0, len(sheet["rows"]))
            cols_total = max(sheet.get("cols_total") or 0, max((len(r) for r in sheet["rows"]), default=0))
            sheets.append({"name": sheet["name"], "rows": rows, "rows_total": rows_total, "cols_total": cols_total,
                           "truncated": rows_total > max_rows or cols_total > PREVIEW_COLS})
        return {"kind": "spreadsheet", "sheets": sheets, "truncated": any(s["truncated"] for s in sheets),
                "warnings": warnings}
    if parsed["family"] == "presentation":
        blocks = []
        for index, slide in enumerate(parsed["slides"], 1):
            blocks.append(heading("幻灯片 %d%s" % (index, "（隐藏）" if slide.get("hidden") else ""), 2))
            blocks.extend(slide["blocks"])
            if slide.get("notes"):
                blocks.append(paragraph("备注：" + slide["notes"]))
    else:
        blocks = parsed["blocks"]
    truncated = len(blocks) > max_blocks or "TEXT_TRUNCATED" in parsed["warnings"]
    out = []
    for block in blocks[:max_blocks]:
        if block["type"] == "table":
            block = {"type": "table", "rows": [[clip(c) for c in row[:PREVIEW_COLS]] for row in block["rows"][:max_rows]]}
        elif block["type"] in ("paragraph", "list_item"):
            block = dict(block, runs=[dict(r, text=clip(r["text"], 5000)) for r in block["runs"]])
        elif block["type"] == "heading":
            block = dict(block, text=clip(block["text"], 1000))
        out.append(block)
    return {"kind": "document", "blocks": out, "header": clip(parsed["header"].strip(), 1000),
            "footer": clip(parsed["footer"].strip(), 1000), "truncated": truncated, "warnings": warnings}


# ---- safe XML ---------------------------------------------------------------------------------------------------

_DOCTYPE = re.compile(rb"<!\s*(DOCTYPE|ENTITY)", re.I)


def safe_xml(data):
    """Parse untrusted XML with the stdlib: DTDs and entity declarations are refused outright (no entity expansion,
    no external resources), which removes billion-laughs and XXE; size is bounded by the caller."""
    import xml.etree.ElementTree as ET
    if _DOCTYPE.search(data[:65536]) or _DOCTYPE.search(data):
        raise ExtractError("UNREADABLE_FILE", "xml with DTD/entities refused")
    try:
        return ET.fromstring(data)
    except ET.ParseError as exc:
        raise ExtractError("UNREADABLE_FILE", "xml cannot be parsed") from exc


def local(tag):
    return tag.rsplit("}", 1)[-1] if isinstance(tag, str) else ""


def open_zip_checked(path):
    """ZipFile with bomb limits: entry count, declared sizes and overall expansion ratio."""
    import zipfile
    try:
        zf = zipfile.ZipFile(path)
    except zipfile.BadZipFile as exc:
        raise ExtractError("UNREADABLE_FILE", "not a zip container") from exc
    infos = zf.infolist()
    if len(infos) > 20000:
        zf.close()
        raise ExtractError("UNREADABLE_FILE", "too many entries")
    total = sum(i.file_size for i in infos)
    packed = sum(i.compress_size for i in infos) or 1
    if total > MAX_UNCOMPRESSED or (total > 64 * 1024 * 1024 and total / packed > 200):
        zf.close()
        raise ExtractError("UNREADABLE_FILE", "container expands too much (possible zip bomb)")
    return zf


def read_part(zf, name, limit=MAX_PART_BYTES):
    """Bounded read of one zip member (declared size is not trusted: the read itself is capped)."""
    try:
        info = zf.getinfo(name)
    except KeyError as exc:
        raise ExtractError("UNREADABLE_FILE", "missing part") from exc
    if info.file_size > limit:
        raise ExtractError("UNREADABLE_FILE", "part too large")
    with zf.open(info) as f:
        data = f.read(limit + 1)
    if len(data) > limit:
        raise ExtractError("UNREADABLE_FILE", "part too large")
    return data
