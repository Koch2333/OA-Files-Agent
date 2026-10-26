"""Readers for Office 97-2003 binary formats (OLE2 compound files): .doc, .ppt, .xls and Outlook .msg.

- olefile (BSD) opens the container; .xls goes through xlrd (BSD, reads .xls only); .doc and .ppt text extraction is
  hand-written here (no maintained permissive pure-Python library exists), following [MS-DOC] and [MS-PPT]:
  .doc = FIB -> Clx piece table -> character stream (+ PAPX table flags); .ppt = record tree + persist directory.
- Conservative by design: when the structure is damaged the readers fall back to a scan and mark the result
  `partial` instead of failing. Nothing is executed: macros, OLE objects and links are never touched.
- Every stream read is bounded; declared sizes are not trusted (a stream cannot be larger than the file).
"""
import bisect
import os
import re
import struct

from fmt_common import (ExtractError, MAX_PART_BYTES, clean, heading, list_item, meta, new_parsed, paragraph,
                        table)

OLE_MAGIC = b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"
MAX_STREAM_TOTAL = 512 * 1024 * 1024
MAX_RECORDS = 3_000_000


def u16(b, o):
    return struct.unpack_from("<H", b, o)[0]


def u32(b, o):
    return struct.unpack_from("<I", b, o)[0]


class Ole:
    """Bounded wrapper around olefile.OleFileIO."""

    def __init__(self, path):
        import olefile
        self.size = os.path.getsize(path)
        self.read_total = 0
        try:
            self.ole = olefile.OleFileIO(path)
        except Exception as exc:
            raise ExtractError("UNREADABLE_FILE", "OLE container cannot be opened") from exc

    def close(self):
        try:
            self.ole.close()
        except Exception:
            pass

    def has(self, name):
        try:
            return self.ole.exists(name)
        except Exception:
            return False

    def streams(self):
        try:
            return ["/".join(s) for s in self.ole.listdir(streams=True, storages=False)]
        except Exception:
            return []

    def read(self, name, limit=MAX_PART_BYTES):
        try:
            if not self.ole.exists(name):
                return None
            declared = self.ole.get_size(name)
            if declared > self.size + 4096 or declared > limit:
                raise ExtractError("UNREADABLE_FILE", "stream size is inconsistent with the file (OLE bomb?)")
            self.read_total += declared
            if self.read_total > MAX_STREAM_TOTAL:
                raise ExtractError("UNREADABLE_FILE", "OLE streams too large in total")
            return self.ole.openstream(name).read(declared)
        except ExtractError:
            raise
        except MemoryError:
            raise
        except Exception as exc:
            raise ExtractError("UNREADABLE_FILE", "OLE stream cannot be read") from exc

    def summary(self):
        """title/author/created/modified from \x05SummaryInformation. Own bounded parser: olefile's get_metadata can
        spin for minutes on a hostile property set."""
        try:
            data = self.read("\x05SummaryInformation", 2 * 1024 * 1024)
            return _summary_info(data) if data else meta()
        except ExtractError:
            return meta()
        except Exception:
            return meta()


def _summary_info(data):
    import datetime
    if len(data) < 48 or u16(data, 0) != 0xFFFE:
        return meta()
    section = u32(data, 44)
    if section + 8 > len(data):
        return meta()
    count = min(u32(data, section + 4), 64)
    codepage = 1252
    values = {}
    for i in range(count):
        o = section + 8 + 8 * i
        if o + 8 > len(data):
            break
        pid, off = u32(data, o), section + u32(data, o + 4)
        if off + 8 > len(data):
            continue
        vt = u32(data, off) & 0xFFFF
        if pid == 1 and vt == 2:
            codepage = u16(data, off + 4)
        elif pid in (2, 4) and vt in (30, 31):
            n = min(u32(data, off + 4), 4096)
            raw = data[off + 8:off + 8 + (n * 2 if vt == 31 else n)]
            try:
                values[pid] = (raw.decode("utf-16-le", "replace") if vt == 31 else
                               raw.decode("cp%d" % codepage if codepage not in (1200, 65001) else "utf-8", "replace")).rstrip("\x00")
            except LookupError:
                values[pid] = raw.decode("cp1252", "replace").rstrip("\x00")
        elif pid in (12, 13) and vt == 64 and off + 12 <= len(data):
            ticks = struct.unpack_from("<Q", data, off + 4)[0]
            try:
                if ticks:
                    values[pid] = (datetime.datetime(1601, 1, 1) + datetime.timedelta(microseconds=ticks // 10)).replace(microsecond=0)
            except (OverflowError, ValueError):
                pass
    return meta(values.get(2), values.get(4), values.get(12), values.get(13))


def sniff_ole(path):
    """-> 'doc' | 'xls' | 'ppt' | 'msg' | 'encrypted_ooxml' | 'ole_other'."""
    ole = Ole(path)
    try:
        if ole.has("EncryptedPackage") or ole.has("EncryptionInfo"):
            return "encrypted_ooxml"
        if ole.has("WordDocument"):
            return "doc"
        if ole.has("Workbook") or ole.has("Book"):
            return "xls"
        if ole.has("PowerPoint Document"):
            return "ppt"
        if ole.has("__properties_version1.0"):
            return "msg"
        return "ole_other"
    finally:
        ole.close()


# ====================================================================== .doc ======================================

_PUA = {0xF0B7: "•", 0xF0A7: "▪", 0xF0D8: "▶", 0xF0FC: "✓", 0xF06C: "●"}


def _fields_and_controls(chars):
    """Resolve field codes (keep results) and Word control characters in a list of characters."""
    out = []
    stack = []   # 'code' | 'result' per open field
    for ch in chars:
        o = ord(ch)
        if o == 0x13:
            stack.append("code")
            continue
        if o == 0x14:
            if stack:
                stack[-1] = "result"
            continue
        if o == 0x15:
            if stack:
                stack.pop()
            continue
        if "code" in stack:
            continue
        if o in (0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x08, 0x1f):
            continue
        if o == 0x0b:
            out.append("\n")
        elif o == 0x0c:
            out.append("\n")
        elif o == 0x1e:
            out.append("-")
        elif 0xF000 <= o <= 0xF0FF:
            out.append(_PUA.get(o, ""))
        elif o < 0x20 and ch not in "\t\r\x07\n":
            continue
        else:
            out.append(ch)
    return out


class _DocText:
    """Character access over the piece table."""

    def __init__(self, wd, cps, pcds):
        self.wd, self.cps, self.pcds = wd, cps, pcds
        self.truncated = False

    def fc_of(self, cp):
        k = bisect.bisect_right(self.cps, cp) - 1
        if k < 0 or k >= len(self.pcds):
            return None
        fc = self.pcds[k]
        return (fc & 0x3FFFFFFF) + 2 * (cp - self.cps[k])

    def chars(self, a, b):
        parts = []
        k = max(0, bisect.bisect_right(self.cps, a) - 1)
        while k < len(self.pcds) and self.cps[k] < b:
            start = max(a, self.cps[k])
            end = min(b, self.cps[k + 1])
            n = end - start
            if n > 0:
                raw = self.pcds[k]
                real = raw & 0x3FFFFFFF
                if raw & 0x40000000:     # compressed: one byte per character (cp1252)
                    off = real // 2 + (start - self.cps[k])
                    chunk = self.wd[off:off + n]
                    if len(chunk) < n:
                        self.truncated = True
                    parts.append(chunk.decode("cp1252", "replace"))
                else:
                    off = real + 2 * (start - self.cps[k])
                    chunk = self.wd[off:off + 2 * n]
                    if len(chunk) < 2 * n:
                        self.truncated = True
                    parts.append(chunk[:len(chunk) // 2 * 2].decode("utf-16-le", "replace"))
            k += 1
        return "".join(parts)


def _sprm_scan(grpprl):
    """-> (in_table, ttp) from a PAPX grpprl; stops at operands whose size is not trivially known."""
    in_table = ttp = None
    j = 0
    while j + 2 <= len(grpprl):
        op = u16(grpprl, j)
        spra = op >> 13
        if spra in (0, 1):
            size = 1
        elif spra in (2, 4, 5):
            size = 2
        elif spra == 3:
            size = 4
        elif spra == 7:
            size = 3
        else:
            break   # variable sized (tab stops, table definitions): not needed
        if j + 2 + size > len(grpprl):
            break
        if op == 0x2416:
            in_table = bool(grpprl[j + 2])
        elif op == 0x2417:
            ttp = bool(grpprl[j + 2])
        j += 2 + size
    return in_table, ttp


def _papx_ranges(wd, tbl, fc, lcb):
    """PlcBtePapx -> sorted list of (fc_start, fc_end, in_table, ttp)."""
    ranges = []
    if not lcb or fc + lcb > len(tbl):
        return None
    plc = tbl[fc:fc + lcb]
    n = (len(plc) - 4) // 8
    if n <= 0 or n > 200000:
        return None
    for i in range(n):
        pn = u32(plc, 4 * (n + 1) + 4 * i) & 0x3FFFFF
        base = pn * 512
        if base + 512 > len(wd):
            continue
        fkp = wd[base:base + 512]
        crun = fkp[511]
        if crun == 0 or 4 * (crun + 1) + 13 * crun > 511:
            continue
        for r in range(crun):
            start, end = u32(fkp, 4 * r), u32(fkp, 4 * (r + 1))
            boff = fkp[4 * (crun + 1) + 13 * r]
            in_table = ttp = None
            if boff:
                p = boff * 2
                if p < 511:
                    cb = fkp[p]
                    if cb:
                        body = fkp[p + 1:p + 1 + 2 * cb - 1]
                    else:
                        cb2 = fkp[p + 1]
                        body = fkp[p + 2:p + 2 + 2 * cb2]
                    in_table, ttp = _sprm_scan(body[2:])
            ranges.append((start, end, in_table, ttp))
    ranges.sort()
    return ranges


def _story_lines(text, header=False):
    lines = []
    for line in re.split(r"[\r\x07\n]+", text):
        line = line.strip()
        if line and line not in lines:
            lines.append(line)
    return lines


def _scan_fallback(wd):
    """Last resort when the piece table is unusable: decode the text region of the stream heuristically."""
    lo = u32(wd, 0x18) if len(wd) >= 0x20 else 0
    hi = u32(wd, 0x1C) if len(wd) >= 0x20 else len(wd)
    if not (0 < lo < hi <= len(wd)):
        lo, hi = 0x200, len(wd)
    region = wd[lo:hi]
    if not region:
        raise ExtractError("UNREADABLE_FILE", "document text not found")
    zeros = region[1::2].count(0)
    if len(region) > 8 and zeros > 0.3 * (len(region) // 2):
        text = region[:len(region) // 2 * 2].decode("utf-16-le", "replace")
    else:
        text = region.decode("cp1252", "replace")
    text = "".join(_fields_and_controls(text))
    # keep runs of plausible text (letters/CJK/punctuation), drop binary noise
    runs = re.findall(r"[^\x00-\x08\x0e-\x1f�]{4,}", text)
    return "\n".join(r.strip() for r in runs if r.strip())


def parse_doc(path, lim):
    ole = Ole(path)
    try:
        wd = ole.read("WordDocument")
        if wd is None or len(wd) < 0x40:
            raise ExtractError("UNREADABLE_FILE", "WordDocument stream missing")
        ident = u16(wd, 0)
        if ident == 0xA5DC or (ident == 0xA5EC and u16(wd, 2) < 0xC0):
            raise ExtractError("UNSUPPORTED_FORMAT", "Word 6.0/95 document")
        if ident != 0xA5EC:
            raise ExtractError("UNREADABLE_FILE", "not a Word document")
        flags = u16(wd, 0x0A)
        if flags & 0x0100 or flags & 0x8000:
            raise ExtractError("ENCRYPTED", "password protected Word document")
        parsed = new_parsed("document")
        parsed["metadata"] = ole.summary()
        try:
            _parse_doc_body(ole, wd, flags, parsed, lim)
        except ExtractError:
            raise
        except (struct.error, IndexError, ValueError, OverflowError, MemoryError):
            parsed["blocks"] = []
            text = _scan_fallback(wd)
            parsed["blocks"] = [paragraph(t) for t in text.split("\n") if t.strip()][:lim["max_blocks"]]
            parsed["partial"] = True
            parsed["warnings"].append("DOC_STRUCTURE_DAMAGED")
        return parsed
    finally:
        ole.close()


def _parse_doc_body(ole, wd, flags, parsed, lim):
    csw = u16(wd, 32)
    off = 34 + csw * 2
    cslw = u16(wd, off)
    lw = off + 2
    ccp = [u32(wd, lw + 4 * i) if i < cslw else 0 for i in range(11)]
    ccp_text, ccp_ftn, ccp_hdd, ccp_mcr, ccp_atn, ccp_edn, ccp_txbx, ccp_hdrtxbx = ccp[3], ccp[4], ccp[5], ccp[6], ccp[7], ccp[8], ccp[9], ccp[10]
    off = lw + cslw * 4
    cb_fclcb = u16(wd, off)
    fcbase = off + 2
    if cb_fclcb < 34:
        raise ValueError("FIB too short")
    fc_papx, lcb_papx = u32(wd, fcbase + 13 * 8), u32(wd, fcbase + 13 * 8 + 4)
    fc_clx, lcb_clx = u32(wd, fcbase + 33 * 8), u32(wd, fcbase + 33 * 8 + 4)
    tbl = ole.read("1Table" if flags & 0x0200 else "0Table")
    if tbl is None:
        raise ValueError("table stream missing")
    if fc_clx + lcb_clx > len(tbl) or lcb_clx < 5:
        raise ValueError("Clx out of range")
    clx = tbl[fc_clx:fc_clx + lcb_clx]
    i = 0
    plc = None
    while i < len(clx):
        tag = clx[i]
        if tag == 1:
            i += 3 + u16(clx, i + 1)
        elif tag == 2:
            n_bytes = u32(clx, i + 1)
            plc = clx[i + 5:i + 5 + n_bytes]
            break
        else:
            raise ValueError("bad Clx")
    if plc is None or len(plc) < 16:
        raise ValueError("no piece table")
    n = (len(plc) - 4) // 12
    cps = [u32(plc, 4 * k) for k in range(n + 1)]
    pcds = [u32(plc, 4 * (n + 1) + 8 * k + 2) for k in range(n)]
    if any(cps[k] > cps[k + 1] for k in range(n)):
        raise ValueError("piece table not monotonic")
    total = ccp_text + ccp_ftn + ccp_hdd + ccp_mcr + ccp_atn + ccp_edn + ccp_txbx + ccp_hdrtxbx
    if total > 64_000_000 or total > cps[-1] + 1:
        parsed["partial"] = True
        parsed["warnings"].append("DOC_TEXT_LENGTH_MISMATCH")
        total = min(total, cps[-1])
        ccp_text = min(ccp_text, total)
    dt = _DocText(wd, cps, pcds)
    ranges = None
    try:
        ranges = _papx_ranges(wd, tbl, fc_papx, lcb_papx)
    except (struct.error, IndexError):
        ranges = None
    starts = [r[0] for r in ranges] if ranges else []

    def flags_at(cp):
        if not ranges:
            return None, None
        fc = dt.fc_of(cp)
        if fc is None:
            return None, None
        k = bisect.bisect_right(starts, fc) - 1
        if k >= 0 and ranges[k][0] <= fc < ranges[k][1]:
            return ranges[k][2], ranges[k][3]
        return None, None

    main = dt.chars(0, ccp_text)
    blocks = []
    rows, cells, cur = [], [], []
    approx_tables = False
    stack = []   # field state while walking (needed to know which chars are dropped)
    pos = 0
    max_blocks = lim["max_blocks"]

    def flush_table():
        nonlocal rows
        if rows:
            blocks.append(table([[clean(c).strip() for c in r] for r in rows]))
            rows = []

    for ch in main:
        o = ord(ch)
        if o == 0x13:
            stack.append("code")
        elif o == 0x14:
            if stack:
                stack[-1] = "result"
        elif o == 0x15:
            if stack:
                stack.pop()
        elif "code" in stack:
            pass
        elif ch == "\r" or ch == "\x07":
            text = "".join(_fields_and_controls(cur))
            in_tbl, ttp = flags_at(pos)
            if ch == "\x07":
                if in_tbl is None and ttp is None:
                    approx_tables = True
                    row_end = (not text.strip()) and bool(cells) and cur_prev_cell_end[0]
                else:
                    row_end = bool(ttp)
                if row_end:
                    rows.append(cells)
                    cells = []
                else:
                    cells.append(text.replace("\n", " "))
                cur_prev_cell_end[0] = not row_end
            else:
                if cells:
                    rows.append(cells)
                    cells = []
                flush_table()
                cur_prev_cell_end[0] = False
                if text.strip():
                    blocks.append(paragraph(clean(text).strip("\n")))
            cur = []
            if len(blocks) >= max_blocks:
                parsed["warnings"].append("TEXT_TRUNCATED")
                break
        else:
            cur.append(ch)
        pos += 1
    if cells:
        rows.append(cells)
    flush_table()
    tail = "".join(_fields_and_controls(cur)).strip()
    if tail and len(blocks) < max_blocks:
        blocks.append(paragraph(clean(tail)))
    parsed["blocks"] = blocks
    if approx_tables:
        parsed["warnings"].append("DOC_TABLE_STRUCTURE_APPROXIMATE")
    # secondary stories
    s = ccp_text
    footnotes = dt.chars(s, s + ccp_ftn)
    s += ccp_ftn
    headers = dt.chars(s, s + ccp_hdd)
    s += ccp_hdd + ccp_mcr + ccp_atn
    endnotes = dt.chars(s, s + ccp_edn)
    s += ccp_edn
    textboxes = dt.chars(s, s + ccp_txbx)
    hf = _story_lines("".join(_fields_and_controls(headers)))
    parsed["header"] = "\n".join(hf)
    for label, story in (("脚注", footnotes), ("尾注", endnotes), ("文本框", textboxes)):
        lines = _story_lines("".join(_fields_and_controls(story)))
        if lines:
            blocks.append(paragraph("[%s] %s" % (label, " / ".join(lines))))
    if dt.truncated:
        parsed["partial"] = True
        parsed["warnings"].append("DOC_STREAM_TRUNCATED")


cur_prev_cell_end = [False]


# ====================================================================== .ppt ======================================

PPT_TEXT_CHARS, PPT_TEXT_BYTES = 0x0FA0, 0x0FA8
REC_SLIDE, REC_NOTES, REC_DOC, REC_SLWT = 0x03EE, 0x03F0, 0x03E8, 0x0FF0
REC_PERSIST_ATOM, REC_TEXT_HDR, REC_CLIENT_TB = 0x03F3, 0x0F9F, 0xF00D
REC_NOTES_ATOM, REC_USER_EDIT, REC_PERSIST_DIR = 0x03F1, 0x0FF5, 0x1772


def _records(buf, start, end, budget, depth=0):
    """Yield (ver, inst, rtype, data_off, length, rec_off); containers (ver 0xF) are NOT descended automatically."""
    pos = start
    while pos + 8 <= end:
        budget[0] -= 1
        if budget[0] < 0:
            raise ExtractError("UNREADABLE_FILE", "too many records")
        vi, rtype, ln = struct.unpack_from("<HHI", buf, pos)
        data = pos + 8
        yield vi & 0xF, vi >> 4, rtype, data, ln, pos
        if data + ln > end:
            return
        pos = data + ln


def _ppt_text(buf, off, ln, rtype):
    raw = buf[off:off + ln]
    if rtype == PPT_TEXT_CHARS:
        text = raw[:len(raw) // 2 * 2].decode("utf-16-le", "replace")
    else:
        text = raw.decode("cp1252", "replace")
    text = text.replace("\r\n", "\n").replace("\r", "\n").replace("\x0b", "\n")
    text = clean(text).strip("\n")
    if len(text.strip()) <= 1 and not text.strip().isalnum():
        return ""        # slide-number / date placeholders and stray markers
    return text


def _collect_texts(buf, start, end, budget, depth=0, only_in_textbox=False, in_tb=False):
    """Text atoms below a container -> list of (text_type or None, text). With only_in_textbox, only atoms inside a
    ClientTextbox (shape text) are returned."""
    out = []
    if depth > 40:
        return out
    last_type = None
    for ver, inst, rtype, data, ln, rec in _records(buf, start, end, budget):
        stop = min(data + ln, end)
        if ver == 0xF:
            out.extend(_collect_texts(buf, data, stop, budget, depth + 1, only_in_textbox, in_tb or rtype == REC_CLIENT_TB))
        elif rtype == REC_TEXT_HDR and ln >= 4:
            last_type = u32(buf, data)
        elif rtype in (PPT_TEXT_CHARS, PPT_TEXT_BYTES):
            if in_tb or not only_in_textbox:
                text = _ppt_text(buf, data, stop - data, rtype)
                if text.strip():
                    out.append((last_type, text))
            last_type = None
    return out


def _persist_map(pd, cu):
    """CurrentUser + UserEditAtom chain + PersistDirectoryAtoms -> ({persistId: offset}, docPersistId) or raise."""
    if len(cu) < 20:
        raise ValueError("no current user")
    token = u32(cu, 12)
    if token == 0xF3D1C4DF:
        raise ExtractError("ENCRYPTED", "password protected presentation")
    off = u32(cu, 16)
    pmap = {}
    seen = set()
    doc_ref = None
    first = True
    while off and off not in seen and len(seen) < 2000 and off + 8 <= len(pd):
        seen.add(off)
        vi, rtype, ln = struct.unpack_from("<HHI", pd, off)
        if rtype != REC_USER_EDIT or ln < 28 or off + 8 + ln > len(pd):
            raise ValueError("bad UserEditAtom")
        d = off + 8
        last_edit, dir_off, doc = u32(pd, d + 8), u32(pd, d + 12), u32(pd, d + 16)
        if ln >= 32 and u32(pd, d + 28):
            raise ExtractError("ENCRYPTED", "password protected presentation")
        if first:
            doc_ref = doc
            first = False
        if dir_off + 8 > len(pd):
            raise ValueError("bad persist directory offset")
        pvi, ptype, pln = struct.unpack_from("<HHI", pd, dir_off)
        if ptype != REC_PERSIST_DIR:
            raise ValueError("bad PersistDirectoryAtom")
        p, pend = dir_off + 8, min(dir_off + 8 + pln, len(pd))
        while p + 4 <= pend:
            head = u32(pd, p)
            pid, cnt = head & 0xFFFFF, head >> 20
            p += 4
            for k in range(cnt):
                if p + 4 > pend:
                    break
                pmap.setdefault(pid + k, u32(pd, p))
                p += 4
        off = last_edit
    if doc_ref is None:
        raise ValueError("no edit")
    return pmap, doc_ref


def parse_ppt(path, lim):
    ole = Ole(path)
    try:
        pd = ole.read("PowerPoint Document")
        if pd is None or len(pd) < 8:
            raise ExtractError("UNREADABLE_FILE", "PowerPoint Document stream missing")
        cu = ole.read("Current User") or b""
        if len(cu) >= 16 and u32(cu, 12) == 0xF3D1C4DF:
            raise ExtractError("ENCRYPTED", "password protected presentation")
        parsed = new_parsed("presentation")
        parsed["metadata"] = ole.summary()
        budget = [MAX_RECORDS]
        try:
            _ppt_structured(pd, cu, parsed, budget, lim)
        except ExtractError:
            raise
        except (ValueError, struct.error, IndexError, KeyError, OverflowError, RecursionError):
            parsed["slides"] = []
            parsed["partial"] = True
            parsed["warnings"].append("PPT_STRUCTURE_FALLBACK")
            _ppt_linear(pd, parsed, [MAX_RECORDS])
        return parsed
    finally:
        ole.close()


def _slide_blocks(items):
    blocks, seen = [], set()
    for ttype, text in items:
        if text in seen:
            continue
        seen.add(text)
        if ttype in (0, 5):
            blocks.append(heading(text.replace("\n", " "), 3))
        else:
            for line in text.split("\n"):
                if line.strip():
                    blocks.append(paragraph(line))
    return blocks


def _slwt(pd, start, end, budget):
    """SlideListWithText children -> list of (persistIdRef, slideIdentifier, [(type,text)])."""
    entries = []
    for ver, inst, rtype, data, ln, rec in _records(pd, start, end, budget):
        stop = min(data + ln, end)
        if rtype == REC_PERSIST_ATOM and ln >= 16:
            entries.append([u32(pd, data), u32(pd, data + 12), []])
        elif rtype == REC_TEXT_HDR and entries and ln >= 4:
            entries[-1].append(("hdr", u32(pd, data)))
        elif rtype in (PPT_TEXT_CHARS, PPT_TEXT_BYTES) and entries:
            hdr = [x for x in entries[-1][3:] if x[0] == "hdr"]
            ttype = hdr[-1][1] if hdr else None
            entries[-1][2].append((ttype, _ppt_text(pd, data, stop - data, rtype)))
            del entries[-1][3:]
    return [(a, b, [t for t in c if t[1].strip()]) for a, b, c, *_ in entries]


def _ppt_structured(pd, cu, parsed, budget, lim):
    pmap, doc_ref = _persist_map(pd, cu)
    doc_off = pmap[doc_ref]
    vi, rtype, ln = struct.unpack_from("<HHI", pd, doc_off)
    if rtype != REC_DOC:
        raise ValueError("document container not found")
    dend = min(doc_off + 8 + ln, len(pd))
    slide_entries, notes_entries = [], []
    for ver, inst, rt, data, l2, rec in _records(pd, doc_off + 8, dend, budget):
        if rt == REC_SLWT:
            entries = _slwt(pd, data, min(data + l2, dend), budget)
            if inst == 0:
                slide_entries = entries
            elif inst == 2:
                notes_entries = entries
    notes_text = {}   # slide identifier -> text
    for pid, _ident, texts in notes_entries:
        try:
            noff = pmap[pid]
            _v, nt, nl = struct.unpack_from("<HHI", pd, noff)
            if nt != REC_NOTES:
                continue
            nend = min(noff + 8 + nl, len(pd))
            slide_ref = None
            for ver, inst, rt, data, l3, rec in _records(pd, noff + 8, nend, budget):
                if rt == REC_NOTES_ATOM and l3 >= 4:
                    slide_ref = u32(pd, data)
                    break
            extra = _collect_texts(pd, noff + 8, nend, budget, only_in_textbox=True)
            items = [t for _ty, t in texts + extra]
            if slide_ref is not None and items:
                notes_text[slide_ref] = "\n".join(dict.fromkeys(items))
        except (struct.error, KeyError):
            parsed["warnings"].append("PPT_NOTES_SKIPPED")
    if not slide_entries:
        raise ValueError("no slide list")
    for pid, ident, texts in slide_entries:
        items = list(texts)
        off = pmap.get(pid)
        if off is not None and off + 8 <= len(pd):
            _v, st, sl = struct.unpack_from("<HHI", pd, off)
            if st == REC_SLIDE:
                items += _collect_texts(pd, off + 8, min(off + 8 + sl, len(pd)), budget, only_in_textbox=True)
        parsed["slides"].append({"blocks": _slide_blocks(items), "notes": notes_text.get(ident, ""), "hidden": False})
        if len(parsed["slides"]) >= 2000:
            parsed["warnings"].append("SLIDE_CAP_REACHED")
            break


def _ppt_linear(pd, parsed, budget):
    """Fallback: slides = Slide containers in stream order; text = every text atom below each (placeholders included
    when they carry text) plus SlideListWithText entries by position."""
    slides = []
    slwt_entries = []
    pos = 0
    for ver, inst, rt, data, ln, rec in _records(pd, 0, len(pd), budget):
        stop = min(data + ln, len(pd))
        if rt == REC_SLIDE:
            slides.append(_collect_texts(pd, data, stop, budget))
        elif rt == REC_DOC:
            for v2, i2, r2, d2, l2, rc2 in _records(pd, data, stop, budget):
                if r2 == REC_SLWT and i2 == 0:
                    slwt_entries = _slwt(pd, d2, min(d2 + l2, stop), budget)
    count = max(len(slides), len(slwt_entries))
    for i in range(min(count, 2000)):
        items = (slwt_entries[i][2] if i < len(slwt_entries) else []) + (slides[i] if i < len(slides) else [])
        parsed["slides"].append({"blocks": _slide_blocks(items), "notes": "", "hidden": False})
    if not parsed["slides"]:
        raise ExtractError("UNREADABLE_FILE", "no slides found")


# ====================================================================== .xls ======================================

def parse_xls(path, lim):
    import io
    import xlrd
    size = os.path.getsize(path)
    ole = None
    summary = meta()
    try:
        if open(path, "rb").read(8) == OLE_MAGIC:
            ole = Ole(path)
            summary = ole.summary()
            if ole.has("EncryptedPackage") or ole.has("EncryptionInfo"):
                raise ExtractError("ENCRYPTED", "password protected workbook")
    finally:
        if ole:
            ole.close()
    parsed = new_parsed("spreadsheet")
    parsed["metadata"] = summary
    try:
        book = xlrd.open_workbook(path, logfile=io.StringIO(), on_demand=True, ragged_rows=True)
    except xlrd.XLRDError as exc:
        msg = str(exc).lower()
        if "encrypted" in msg or "password" in msg:
            raise ExtractError("ENCRYPTED", "password protected workbook") from exc
        raise ExtractError("UNREADABLE_FILE", "xls cannot be parsed") from exc
    except (ValueError, struct.error, IndexError, KeyError, AssertionError, OverflowError, RecursionError) as exc:
        raise ExtractError("UNREADABLE_FILE", "xls cannot be parsed") from exc
    try:
        max_rows = lim["max_rows"]
        for i in range(book.nsheets):
            try:
                sh = book.sheet_by_index(i)
                rows = []
                for r in range(min(sh.nrows, max_rows)):
                    rows.append([_xls_cell(book, c)[:] if False else _xls_cell(book, c) for c in sh.row(r)[:200]])
                entry = {"name": sh.name, "rows": rows, "hidden": sh.visibility != 0, "rows_total": sh.nrows,
                         "cols_total": sh.ncols}
                if sh.nrows > max_rows:
                    parsed["warnings"].append("ROW_CAP_REACHED:" + sh.name)
                parsed["sheets"].append(entry)
                book.unload_sheet(i)
            except (xlrd.XLRDError, struct.error, IndexError, ValueError, KeyError) as exc:
                parsed["partial"] = True
                parsed["warnings"].append("XLS_SHEET_UNREADABLE:%d" % (i + 1))
        if not parsed["sheets"]:
            raise ExtractError("UNREADABLE_FILE", "no readable sheets")
    finally:
        book.release_resources()
    hidden = [s["name"] for s in parsed["sheets"] if s["hidden"]]
    if hidden:
        parsed["warnings"].append("HIDDEN_SHEETS:" + ",".join(hidden))
    return parsed


def _xls_cell(book, cell):
    import xlrd
    from fmt_common import cell_text
    t, v = cell.ctype, cell.value
    if t in (xlrd.XL_CELL_EMPTY, xlrd.XL_CELL_BLANK):
        return ""
    if t == xlrd.XL_CELL_DATE:
        try:
            return cell_text(xlrd.xldate.xldate_as_datetime(v, book.datemode))
        except Exception:
            return cell_text(v)
    if t == xlrd.XL_CELL_BOOLEAN:
        return "TRUE" if v else "FALSE"
    if t == xlrd.XL_CELL_ERROR:
        return xlrd.error_text_from_code.get(v, "#ERR")
    return clean(cell_text(v))


# ====================================================================== .msg ======================================

def parse_msg(path, lim):
    ole = Ole(path)
    try:
        def text_prop(prop):
            for suffix, enc in (("001F", "utf-16-le"), ("001E", "cp1252")):
                data = ole.read("__substg1.0_%s%s" % (prop, suffix), 8 * 1024 * 1024)
                if data is not None:
                    return clean(data.decode(enc, "replace").rstrip("\x00"))
            return None
        parsed = new_parsed("document")
        subject, sender, to, cc = text_prop("0037"), text_prop("0C1A") or text_prop("0042"), text_prop("0E04"), text_prop("0E03")
        body = text_prop("1000")
        if not body:
            html = ole.read("__substg1.0_10130102", 8 * 1024 * 1024)
            if html:
                from text_formats import html_to_blocks
                parsed["blocks"] = html_to_blocks(html.decode("utf-8", "replace"))[0]
                body = ""
        head = [("主题", subject), ("发件人", sender), ("收件人", to), ("抄送", cc)]
        parsed["blocks"] = [paragraph("%s：%s" % (k, v)) for k, v in head if v] + parsed["blocks"]
        for line in (body or "").replace("\r\n", "\n").replace("\r", "\n").split("\n"):
            if line.strip():
                parsed["blocks"].append(paragraph(line))
        names = []
        for s in ole.streams():
            if s.startswith("__attach_version1.0_") and s.endswith(("__substg1.0_3707001F", "__substg1.0_3001001F")):
                data = ole.read(s, 64 * 1024)
                if data:
                    names.append(clean(data.decode("utf-16-le", "replace").rstrip("\x00")))
        if names:
            parsed["blocks"].append(paragraph("附件：" + "、".join(dict.fromkeys(names))))
        if not parsed["blocks"]:
            raise ExtractError("UNREADABLE_FILE", "message has no readable properties")
        parsed["metadata"] = meta(subject, sender)
        return parsed
    finally:
        ole.close()
