"""Text-like formats: plain text / Markdown / CSV / TSV (with encoding detection), HTML (visible text only),
RTF (hand-written reader) and e-mail (.eml via the stdlib). Nothing is executed or fetched: scripts, styles and
remote references in HTML are ignored, RTF objects/pictures are skipped, attachments are only listed by name."""
import csv
import io
import re

from fmt_common import (MAX_COLS, ExtractError, cell_text, clean, heading, list_item, meta, new_parsed, paragraph,
                        table)

MAX_TEXT_BYTES = 64 * 1024 * 1024


# ---------------------------------------------------------------------------------------------------- encodings

def _gb_ratio(text_bytes):
    """Share of double-byte sequences that fall in the GB2312 hanzi area (lead B0-F7, trail A1-FE)."""
    pairs = good = 0
    i = 0
    n = len(text_bytes)
    while i < n - 1:
        b = text_bytes[i]
        if b >= 0x81:
            pairs += 1
            if 0xB0 <= b <= 0xF7 and 0xA1 <= text_bytes[i + 1] <= 0xFE:
                good += 1
            i += 2
        else:
            i += 1
    return (good / pairs) if pairs else 1.0


def decode_text(data):
    """-> (text, encoding, low_confidence). BOM, UTF-16 heuristics, strict UTF-8, GB18030, Big5, then cp1252."""
    if data.startswith(b"\xef\xbb\xbf"):
        return data[3:].decode("utf-8", "replace"), "utf-8-sig", False
    if data.startswith((b"\xff\xfe\x00\x00", b"\x00\x00\xfe\xff")):
        return data.decode("utf-32", "replace"), "utf-32", False
    if data.startswith((b"\xff\xfe", b"\xfe\xff")):
        return data.decode("utf-16", "replace"), "utf-16", False
    head = data[:4096]
    if len(head) >= 4 and b"\x00" in head:
        even, odd = head[0::2].count(0), head[1::2].count(0)
        half = max(1, len(head) // 2)
        if odd > 0.4 * half and even < 0.05 * half:
            return data.decode("utf-16-le", "replace"), "utf-16-le", False
        if even > 0.4 * half and odd < 0.05 * half:
            return data.decode("utf-16-be", "replace"), "utf-16-be", False
    try:
        return data.decode("utf-8"), "utf-8", False
    except UnicodeDecodeError:
        pass
    sample = data[:200000]
    try:
        text = data.decode("gb18030")
        if _gb_ratio(sample) >= 0.8:
            return text, "gb18030", False
    except UnicodeDecodeError:
        pass
    try:
        text = data.decode("big5")
        return text, "big5", True
    except UnicodeDecodeError:
        pass
    try:
        text = data.decode("gb18030")
        return text, "gb18030", True
    except UnicodeDecodeError:
        pass
    return data.decode("cp1252", "replace"), "cp1252", True


def looks_binary(data):
    """True when the bytes are very unlikely to be text (NUL bytes without a UTF-16 pattern, many control chars)."""
    head = data[:8192]
    if head.startswith((b"\xff\xfe", b"\xfe\xff", b"\xef\xbb\xbf")):
        return False
    if b"\x00" in head:
        even, odd = head[0::2].count(0), head[1::2].count(0)
        half = max(1, len(head) // 2)
        return not (odd > 0.4 * half and even < 0.05 * half or even > 0.4 * half and odd < 0.05 * half)
    ctrl = sum(1 for b in head if b < 9 or 13 < b < 32 and b != 27)
    return ctrl > 0.1 * max(1, len(head))


def _read_text_file(path):
    with open(path, "rb") as f:
        data = f.read(MAX_TEXT_BYTES + 1)
    parsed_note = len(data) > MAX_TEXT_BYTES
    return data[:MAX_TEXT_BYTES], parsed_note


# ---------------------------------------------------------------------------------------------------- txt / md

_MD_HEADING = re.compile(r"^(#{1,6})\s+(.*\S)\s*#*\s*$")
_MD_LIST = re.compile(r"^(\s*)([-*+]|\d+[.)])\s+(.*)$")


def parse_text(path, lim, markdown=False):
    data, cut = _read_text_file(path)
    text, enc, low = decode_text(data)
    parsed = new_parsed("document")
    parsed["low_confidence"] = low
    parsed["warnings"].append("ENCODING:" + enc)
    if cut:
        parsed["warnings"].append("TEXT_TRUNCATED")
    for raw in clean(text).replace("\r\n", "\n").replace("\r", "\n").split("\n"):
        line = raw.rstrip()
        if not line.strip():
            continue
        m = _MD_HEADING.match(line) if markdown else None
        if m:
            parsed["blocks"].append(heading(m.group(2), len(m.group(1))))
            continue
        m = _MD_LIST.match(line) if markdown else None
        if m:
            parsed["blocks"].append(list_item(m.group(3), min(4, len(m.group(1).expandtabs(4)) // 2)))
            continue
        parsed["blocks"].append(paragraph(line))
        if len(parsed["blocks"]) >= lim["max_blocks"]:
            parsed["warnings"].append("TEXT_TRUNCATED")
            break
    return parsed


# ---------------------------------------------------------------------------------------------------- csv / tsv

def parse_csv(path, lim, delimiter=None, name="Sheet1"):
    data, cut = _read_text_file(path)
    text, enc, low = decode_text(data)
    parsed = new_parsed("spreadsheet")
    parsed["low_confidence"] = low
    parsed["warnings"].append("ENCODING:" + enc)
    text = clean(text)
    sample = "\n".join(text.split("\n", 20)[:20])
    if delimiter is None:
        try:
            delimiter = csv.Sniffer().sniff(sample, delimiters=",;\t|").delimiter
        except csv.Error:
            delimiter = ","
    csv.field_size_limit(1 << 20)
    rows, total, widest = [], 0, 0
    try:
        for row in csv.reader(io.StringIO(text, newline=""), delimiter=delimiter):
            total += 1
            widest = max(widest, len(row))
            if len(rows) < lim["max_rows"]:
                rows.append([c.strip() for c in row[:MAX_COLS]])
    except csv.Error:
        parsed["partial"] = True
        parsed["warnings"].append("CSV_MALFORMED")
    if total > lim["max_rows"]:
        parsed["warnings"].append("ROW_CAP_REACHED:" + name)
    if cut:
        parsed["warnings"].append("TEXT_TRUNCATED")
    parsed["sheets"].append({"name": name, "rows": rows, "hidden": False, "rows_total": total, "cols_total": widest})
    return parsed


# ---------------------------------------------------------------------------------------------------- html

_SKIP = {"script", "style", "noscript", "template", "head", "svg", "iframe", "object", "embed", "canvas", "math", "select"}
_BLOCK = {"p", "div", "section", "article", "header", "footer", "main", "nav", "aside", "blockquote", "pre", "ul", "ol",
          "dl", "dt", "dd", "form", "fieldset", "figure", "figcaption", "address", "hr", "tr_", "br"}
_VOID = {"br", "hr", "img", "input", "meta", "link", "area", "base", "col", "embed", "source", "track", "wbr"}


def html_to_blocks(html, max_blocks=200000):
    """-> (blocks, title). Visible text only; tables become table blocks (nested tables are flattened into the cell)."""
    from html.parser import HTMLParser

    class P(HTMLParser):
        def __init__(self):
            super().__init__(convert_charrefs=True)
            self.blocks = []
            self.title = None
            self.skip = 0
            self.in_title = False
            self.buf = []
            self.heading = None
            self.list_depth = 0
            self.in_li = False
            self.table_depth = 0
            self.rows = []
            self.cells = []
            self.cell = None
            self.span_stack = []

        def flush(self):
            text = re.sub(r"[ \t\r\f\v\u00a0]+", " ", "".join(self.buf)).strip()
            self.buf = []
            if text and len(self.blocks) < max_blocks:
                for line in [t.strip() for t in text.split("\n") if t.strip()]:
                    if self.heading:
                        self.blocks.append(heading(line, self.heading))
                    elif self.in_li:
                        self.blocks.append(list_item(line, max(0, self.list_depth - 1)))
                    else:
                        self.blocks.append(paragraph(line))

        def handle_starttag(self, tag, attrs):
            if tag == "title":
                self.in_title = True
            if tag in _SKIP and tag not in _VOID:
                self.skip += 1
                return
            if tag == "span":
                style = dict(attrs).get("style", "").lower().replace(" ", "").replace("'", "").replace('"', "")
                ignore = ("mso-list:ignore" in style or "font-family:wingdings" in style or "font-family:symbol" in style
                          or "font-family:webdings" in style)
                self.span_stack.append(ignore)
                if ignore:
                    self.skip += 1
                    return
            if self.skip:
                return
            if tag == "table":
                if self.table_depth == 0:
                    self.flush()
                    self.rows = []
                self.table_depth += 1
            elif tag == "tr" and self.table_depth == 1:
                self.cells = []
            elif tag in ("td", "th") and self.table_depth == 1:
                self.cell = []
            elif tag == "br":
                (self.cell if self.cell is not None else self.buf).append("\n")
            elif self.table_depth:
                if tag in ("p", "div", "li"):
                    (self.cell if self.cell is not None else self.buf).append(" ")
            elif re.fullmatch(r"h[1-6]", tag):
                self.flush()
                self.heading = int(tag[1])
            elif tag in ("ul", "ol"):
                self.flush()
                self.list_depth += 1
            elif tag == "li":
                self.flush()
                self.in_li = True
            elif tag in _BLOCK:
                self.flush()

        def handle_endtag(self, tag):
            if tag == "title":
                self.in_title = False
            if tag in _SKIP and tag not in _VOID:
                self.skip = max(0, self.skip - 1)
                return
            if tag == "span" and self.span_stack and self.span_stack.pop():
                self.skip = max(0, self.skip - 1)
                return
            if self.skip:
                return
            if tag in ("td", "th") and self.table_depth == 1 and self.cell is not None:
                self.cells.append(re.sub(r"\s+", " ", "".join(self.cell)).strip())
                self.cell = None
            elif tag == "tr" and self.table_depth == 1:
                if self.cell is not None:
                    self.cells.append(re.sub(r"\s+", " ", "".join(self.cell)).strip())
                    self.cell = None
                if any(self.cells):
                    self.rows.append(self.cells)
                self.cells = []
            elif tag == "table" and self.table_depth:
                self.table_depth -= 1
                if self.table_depth == 0 and self.rows and len(self.blocks) < max_blocks:
                    self.blocks.append(table(self.rows))
                    self.rows = []
            elif self.table_depth:
                pass
            elif re.fullmatch(r"h[1-6]", tag):
                self.flush()
                self.heading = None
            elif tag in ("ul", "ol"):
                self.flush()
                self.list_depth = max(0, self.list_depth - 1)
                self.in_li = self.list_depth > 0 and self.in_li
            elif tag == "li":
                self.flush()
                self.in_li = False
            elif tag in _BLOCK:
                self.flush()

        def handle_data(self, data):
            if self.in_title:
                self.title = ((self.title or "") + data).strip()[:300]
                return
            if self.skip:
                return
            if self.table_depth:
                if self.cell is not None:
                    self.cell.append(data)
                return
            self.buf.append(re.sub(r"[\r\n\t]+", " ", data))

        def error(self, message):  # pragma: no cover - py<3.10 API
            pass

    p = P()
    try:
        p.feed(html)
        p.close()
    except Exception as exc:  # HTMLParser is lenient; anything raised is treated as damaged markup
        raise ExtractError("UNREADABLE_FILE", "html cannot be parsed") from exc
    p.flush()
    if p.rows and len(p.blocks) < max_blocks:
        p.blocks.append(table(p.rows))
    return p.blocks, p.title


_META_CHARSET = re.compile(rb"<meta[^>]+charset\s*=\s*[\"']?\s*([A-Za-z0-9_\-]+)", re.I)


def parse_html(path, lim):
    data, cut = _read_text_file(path)
    enc_hint = None
    m = _META_CHARSET.search(data[:4096])
    if m:
        enc_hint = m.group(1).decode("ascii", "ignore").lower()
    if enc_hint and not data.startswith((b"\xef\xbb\xbf", b"\xff\xfe", b"\xfe\xff")):
        import codecs
        try:
            codecs.lookup(enc_hint)
            text, enc, low = data.decode(enc_hint, "replace"), enc_hint, False
        except (LookupError, ValueError):
            text, enc, low = decode_text(data)
    else:
        text, enc, low = decode_text(data)
    parsed = new_parsed("document")
    parsed["low_confidence"] = low
    parsed["warnings"].append("ENCODING:" + enc)
    blocks, title = html_to_blocks(clean(text), lim["max_blocks"])
    parsed["blocks"] = blocks
    parsed["metadata"] = meta(title=title)
    if cut:
        parsed["warnings"].append("TEXT_TRUNCATED")
    return parsed


# ---------------------------------------------------------------------------------------------------- rtf

_RTF_TOKEN = re.compile(rb"\\([a-zA-Z]{1,32})(-?\d{1,10})? ?|\\'([0-9a-fA-F]{2})|\\([^a-zA-Z])|([{}])|([^\\{}]+)")
_CHARSET_CP = {0: None, 1: None, 128: "cp932", 129: "cp949", 134: "cp936", 136: "cp950", 161: "cp1253", 162: "cp1254",
               177: "cp1255", 178: "cp1256", 186: "cp1257", 204: "cp1251", 238: "cp1250"}
_RTF_SKIP = {"fonttbl", "colortbl", "stylesheet", "pict", "object", "header", "headerl", "headerr", "headerf",
             "footer", "footerl", "footerr", "footerf", "footnote", "fldinst", "datafield", "themedata", "colorschememapping",
             "latentstyles", "listtable", "listoverridetable", "rsidtbl", "generator", "private", "xmlnstbl", "wgrffmtfilter",
             "mmathPr", "pgdsctbl", "revtbl", "falt", "panose", "bkmkstart", "bkmkend", "shpinst", "nonshppict", "blipuid",
             "background", "userprops", "filetbl", "info", "listtext", "pntext", "pn", "ud_skip"}
_SYMBOLS = {"emdash": "\u2014", "endash": "\u2013", "bullet": "\u2022", "lquote": "\u2018", "rquote": "\u2019",
            "ldblquote": "\u201c", "rdblquote": "\u201d", "tab": "\t", "emspace": "\u2003", "enspace": "\u2002",
            "qmspace": "\u2005", "~": "\u00a0", "_": "-"}


def rtf_to_parsed(data, lim):
    parsed = new_parsed("document")
    if not data.lstrip()[:5] == b"{\\rtf":
        raise ExtractError("UNREADABLE_FILE", "not an RTF document")
    blocks = parsed["blocks"]
    stack = []                  # (skip, uc, font_cp)
    skip = 0                    # nesting depth of a skipped destination (0 = emitting)
    uc = 1
    cur_cp = "cp1252"
    default_cp = "cp1252"
    fonts = {}                  # font number -> codepage
    font_cp = None
    cur_font = None
    in_fonttbl = False
    pending = bytearray()
    para, cell, cells, rows = [], [], [], []
    skip_chars = 0
    in_info = None

    def flush_pending():
        nonlocal pending
        if pending:
            cp = font_cp or default_cp
            try:
                text = pending.decode(cp)
            except (UnicodeDecodeError, LookupError):
                text = pending.decode("cp1252", "replace")
            (cell if cell_mode[0] else para).append(text)
            pending = bytearray()

    cell_mode = [False]

    def emit(text):
        if skip or not text:
            return
        flush_pending()
        (cell if cell_mode[0] else para).append(text)

    def end_para():
        flush_pending()
        text = clean("".join(para)).strip()
        para.clear()
        if text and len(blocks) < lim["max_blocks"]:
            blocks.append(paragraph(text))

    def end_cell():
        flush_pending()
        cells.append(clean("".join(cell)).strip().replace("\n", " "))
        cell.clear()

    def end_row():
        nonlocal cells
        if cell:
            end_cell()
        if cells:
            rows.append(cells)
        cells = []

    def flush_rows():
        nonlocal rows
        if rows:
            blocks.append(table(rows))
            rows = []

    pos = 0
    ntok = 0
    for m in _RTF_TOKEN.finditer(data):
        ntok += 1
        if ntok > 20_000_000:
            parsed["partial"] = True
            parsed["warnings"].append("RTF_TOKEN_CAP")
            break
        word, param, hexv, sym, brace, text = m.groups()
        if brace == b"{":
            stack.append((skip, uc, font_cp, in_fonttbl, in_info))
            if skip:
                skip += 1
            continue
        if brace == b"}":
            if stack:
                skip, uc, font_cp, in_fonttbl, in_info = stack.pop()
            continue
        if text is not None:
            if skip or in_fonttbl:
                continue
            chunk = text
            if skip_chars:
                n = min(skip_chars, len(chunk))
                chunk, skip_chars = chunk[n:], skip_chars - n
            if chunk:
                flush_pending()
                chunk = chunk.replace(b"\r", b"").replace(b"\n", b"")
                emit(chunk.decode("cp1252", "replace"))
            continue
        if hexv is not None:
            if skip_chars:
                skip_chars -= 1
                continue
            if not skip and not in_fonttbl:
                pending.append(int(hexv, 16))
            continue
        if sym is not None:
            sym = sym.decode("latin-1")
            if sym == "*":
                # ignorable destination: skip the rest of this group
                skip = skip or 1
                continue
            if sym in ("~", "_"):
                emit(_SYMBOLS[sym])
            elif sym in ("\n", "\r"):
                if not skip:
                    end_para()
            elif sym in ("\\", "{", "}"):
                emit(sym)
            continue
        # control word
        name = word.decode("ascii")
        val = int(param) if param is not None else None
        if skip:
            continue
        if name in _RTF_SKIP:
            if name == "fonttbl":
                in_fonttbl = True
            else:
                skip = 1
            continue
        if in_fonttbl:
            if name == "f" and val is not None:
                cur_font = val
            elif name == "fcharset" and val is not None and cur_font is not None:
                fonts[cur_font] = _CHARSET_CP.get(val)
            continue
        if name == "ansicpg" and val:
            default_cp = "cp%d" % val
        elif name == "f" and val is not None:
            flush_pending()
            font_cp = fonts.get(val)
        elif name == "uc" and val is not None:
            uc = val
        elif name == "u" and val is not None:
            flush_pending()
            code = val + 65536 if val < 0 else val
            emit(chr(code) if code < 0x110000 and not 0xD800 <= code <= 0xDFFF else "\ufffd")
            skip_chars = uc
        elif name in ("par", "sect", "page"):
            if cell_mode[0]:
                emit("\n")
            else:
                end_para()
        elif name == "line":
            emit("\n")
        elif name in _SYMBOLS:
            emit(_SYMBOLS[name])
        elif name == "intbl":
            if not cell_mode[0]:
                end_para()
                cell_mode[0] = True
        elif name == "cell":
            cell_mode[0] = True
            end_cell()
        elif name == "row":
            end_row()
            cell_mode[0] = False
        elif name == "pard" and not cell_mode[0]:
            pass
        elif name == "trowd":
            if not cell_mode[0]:
                end_para()
            cell_mode[0] = True
        if name == "par" and rows and not cell_mode[0]:
            flush_rows()
        if cell_mode[0] is False and rows:
            flush_rows()
        if len(blocks) >= lim["max_blocks"]:
            parsed["warnings"].append("TEXT_TRUNCATED")
            break
    if cell_mode[0]:
        end_row()
    flush_pending()
    flush_rows()
    end_para()
    # table blocks are appended when the table ends; keep document order by re-sorting is unnecessary because rows
    # are flushed before the next paragraph is emitted.
    if not blocks and data.strip() and not parsed["partial"]:
        parsed["warnings"].append("RTF_EMPTY")
    return parsed


def parse_rtf(path, lim):
    data, cut = _read_text_file(path)
    parsed = rtf_to_parsed(data, lim)
    if cut:
        parsed["warnings"].append("TEXT_TRUNCATED")
    return parsed


# ---------------------------------------------------------------------------------------------------- eml

def parse_eml(path, lim):
    from email import policy
    from email.parser import BytesParser
    data, cut = _read_text_file(path)
    try:
        msg = BytesParser(policy=policy.default).parsebytes(data)
    except Exception as exc:
        raise ExtractError("UNREADABLE_FILE", "e-mail cannot be parsed") from exc
    parsed = new_parsed("document")

    def header(name):
        try:
            return clean(str(msg[name] or "")).strip()
        except Exception:
            return clean(str(msg.get_all(name, [""])[0])).strip()

    for label, key in (("主题", "Subject"), ("发件人", "From"), ("收件人", "To"), ("抄送", "Cc"), ("日期", "Date")):
        value = header(key)
        if value:
            parsed["blocks"].append(paragraph("%s：%s" % (label, value)))
    body_text = ""
    try:
        part = msg.get_body(preferencelist=("plain", "html"))
    except Exception:
        part = None
    if part is not None:
        try:
            content = part.get_content()
        except Exception:
            content = ""
        if part.get_content_type() == "text/html":
            blocks, _ = html_to_blocks(clean(content), lim["max_blocks"])
            parsed["blocks"].extend(blocks)
        else:
            body_text = clean(content)
    for line in body_text.replace("\r\n", "\n").split("\n"):
        if line.strip():
            parsed["blocks"].append(paragraph(line.rstrip()))
    names = []
    for att in msg.iter_attachments():
        fn = att.get_filename()
        if fn:
            names.append(clean(fn))
    if names:
        parsed["blocks"].append(paragraph("附件：" + "、".join(list(dict.fromkeys(names))[:50])))
    created = None
    try:
        created = msg["Date"].datetime if msg["Date"] else None
    except Exception:
        created = None
    parsed["metadata"] = meta(header("Subject"), header("From"), created)
    if cut:
        parsed["warnings"].append("TEXT_TRUNCATED")
    return parsed
