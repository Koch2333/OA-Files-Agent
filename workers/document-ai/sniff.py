"""Content-based format detection. The extension is only a claim: the first bytes (and, for containers, the member
names) decide which extractor runs. Mismatches are reported, never trusted blindly.

detect(path, ext) -> {"detected": id, "route": extractor id, "mismatch": bool, "claimed": extractor id or None,
                      "error": (code, message) or None}
ids: pdf, image, docx, xlsx, pptx, odt, ods, odp, doc, xls, ppt, msg, rtf, html, eml, text, csv(claimed only)
     and the non-routable diagnostics encrypted_office, zip_other, zip_broken, ole_other, binary, empty.
"""
import os
import re

import formats_data
from text_formats import looks_binary

TABLE = {f["ext"]: f for f in formats_data.TABLE["formats"]}
EXTRACTOR_OF = {ext: f["extractor"] for ext, f in TABLE.items()}
OOXML = {"docx", "xlsx", "pptx"}
BINARY_FAMILIES = OOXML | {"pdf", "image", "doc", "xls", "ppt", "odt", "ods", "odp", "msg", "ole"}
TEXTLIKE = {"text", "csv", "html", "eml", "rtf_text"}
_HTML = re.compile(rb"<\s*(!doctype\s+html|html|head|body|table|div|p|span|meta|h[1-6]|ul|font|center)\b", re.I)
_EML = re.compile(rb"\A(?:(?:From |[A-Za-z][A-Za-z0-9\-]{1,40}:[^\r\n]*)\r?\n)+", re.I)
_EML_KEYS = re.compile(rb"^(From|To|Subject|Date|Received|Return-Path|MIME-Version|Message-ID|Content-Type):", re.I | re.M)


def _image_kind(head):
    if head.startswith(b"\x89PNG\r\n\x1a\n") or head[:3] == b"\xff\xd8\xff" or head[:6] in (b"GIF87a", b"GIF89a") \
            or head[:2] == b"BM" or head[:4] in (b"II*\x00", b"MM\x00*") or (head[:4] == b"RIFF" and head[8:12] == b"WEBP"):
        return True
    return False


def detect_content(path, claimed_ext):
    """-> detected id (see module doc) from the content only."""
    size = os.path.getsize(path)
    if size == 0:
        return "empty"
    with open(path, "rb") as f:
        head = f.read(65536)
    if b"%PDF-" in head[:1024]:
        return "pdf"
    if _image_kind(head) and not (head[:2] == b"BM" and not _bmp_plausible(head)):
        return "image"
    if head[:4] == b"PK\x03\x04" or head[:4] == b"PK\x05\x06":
        from zip_formats import sniff_zip
        return sniff_zip(path)
    if head[:8] == b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1":
        from ole_formats import sniff_ole
        try:
            return sniff_ole(path)
        except Exception:
            return "ole_other"
    stripped = head.lstrip(b"\xef\xbb\xbf \t\r\n")
    if stripped[:5] == b"{\\rtf":
        return "rtf"
    if looks_binary(head):
        return "binary"
    sample = head[:8192]
    if sample.startswith((b"\xff\xfe", b"\xfe\xff")) or b"\x00" in sample:
        try:
            sample = sample.decode("utf-16", "ignore").encode("utf-8", "ignore")
        except Exception:
            pass
    if _HTML.search(sample[:4096]) and stripped[:1] in (b"<", b""):
        return "html"
    if _HTML.search(sample[:2048]) and sample.count(b"<") > 3:
        return "html"
    first = sample.split(b"\n\n", 1)[0] if b"\n\n" in sample else sample.split(b"\r\n\r\n", 1)[0]
    if _EML.match(sample) and len(_EML_KEYS.findall(first)) >= 2:
        return "eml"
    return "text"


def _bmp_plausible(head):
    # "BM" plus a plausible DIB header size at offset 14
    return len(head) > 18 and int.from_bytes(head[14:18], "little") in (12, 40, 52, 56, 64, 108, 124)


def detect(path, ext):
    """Combine the claimed extension with the content verdict."""
    ext = (ext or "").lower()
    claimed = EXTRACTOR_OF.get(ext)
    detected = detect_content(path, ext)
    out = {"detected": detected, "route": None, "mismatch": False, "claimed": claimed, "error": None}
    if detected == "empty":
        out["error"] = ("UNREADABLE_FILE", "empty file")
        return out
    if detected == "encrypted_office":
        out["error"] = ("ENCRYPTED", "password protected office file")
        return out
    if detected == "zip_broken":
        out["error"] = ("UNREADABLE_FILE", "damaged zip container") if claimed in BINARY_FAMILIES else \
            ("UNSUPPORTED_FORMAT", "unsupported format")
        return out
    if detected == "zip_other":
        out["error"] = ("UNSUPPORTED_FORMAT", "archive or unknown zip package")
        return out
    if detected == "ole_other":
        if claimed in OOXML:
            out["error"] = ("ENCRYPTED", "password protected office file")
        elif claimed in ("doc", "xls", "ppt", "ole", "msg"):
            out["error"] = ("UNREADABLE_FILE", "OLE container has no recognisable Office streams")
        else:
            out["error"] = ("UNSUPPORTED_FORMAT", "unsupported OLE document")
        return out
    if detected == "binary":
        if claimed in BINARY_FAMILIES:
            out["error"] = ("UNREADABLE_FILE", "content does not match the file type (damaged or not a document)")
        else:
            out["error"] = ("UNSUPPORTED_FORMAT", "unsupported format")
        return out
    # recognised content
    if claimed is None:
        if detected in TEXTLIKE | {"rtf"} and detected != "rtf":
            out["error"] = ("UNSUPPORTED_FORMAT", "unsupported format")
            return out
        out.update(route=detected, mismatch=True)
        return out
    if claimed in ("text", "csv", "html", "eml"):
        if detected == "text":
            out.update(route=claimed)
        elif detected == "html":
            if claimed in ("csv", "html"):
                out.update(route="html", mismatch=claimed != "html")
            else:
                out.update(route=claimed)
        elif detected == "eml":
            if claimed in ("csv", "html", "eml"):
                out.update(route="eml", mismatch=claimed != "eml")
            else:
                out.update(route=claimed)
        else:
            out.update(route=detected, mismatch=True)
        return out
    if claimed == "ole":      # WPS: any Office-compatible OLE content is acceptable, nothing else
        if detected in ("doc", "xls", "ppt"):
            out.update(route=detected, mismatch=False)
        elif detected in OOXML | {"odt", "ods", "odp", "rtf", "html", "text"}:
            out.update(route=detected, mismatch=True)
        else:
            out["error"] = ("UNSUPPORTED_FORMAT", "WPS-native format is not supported (save as .doc/.xls/.ppt or .docx/.xlsx/.pptx)")
        return out
    if detected == "text" and claimed in BINARY_FAMILIES | {"rtf"}:
        # plain text hiding behind an Office/PDF/image extension: readable, but flagged
        out.update(route="text", mismatch=True)
        return out
    if detected == claimed:
        out.update(route=detected)
        return out
    out.update(route=detected, mismatch=True)
    return out
