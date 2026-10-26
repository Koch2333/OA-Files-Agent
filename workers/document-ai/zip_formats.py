"""ZIP+XML formats read with the stdlib only: OpenDocument (.odt/.ods/.odp) and PowerPoint (.pptx).
XML is parsed with DTD/entity declarations refused (fmt_common.safe_xml); zip members are read with hard size caps and
the container is checked for bombs first. Macros, embedded objects and external links are never followed."""
import re
import zipfile

from fmt_common import (MAX_COLS, ExtractError, cell_text, clean, heading, list_item, local, meta, new_parsed,
                        open_zip_checked, paragraph, read_part, safe_xml, table)

NS_TEXT = "{urn:oasis:names:tc:opendocument:xmlns:text:1.0}"
NS_TABLE = "{urn:oasis:names:tc:opendocument:xmlns:table:1.0}"
NS_OFFICE = "{urn:oasis:names:tc:opendocument:xmlns:office:1.0}"
NS_DRAW = "{urn:oasis:names:tc:opendocument:xmlns:drawing:1.0}"
NS_STYLE = "{urn:oasis:names:tc:opendocument:xmlns:style:1.0}"
NS_PRES = "{urn:oasis:names:tc:opendocument:xmlns:presentation:1.0}"
MAX_REPEAT = 200


def odf_kind(zf):
    """'odt' | 'ods' | 'odp' | None from the `mimetype` member."""
    try:
        mt = read_part(zf, "mimetype", 256).decode("ascii", "ignore").strip()
    except ExtractError:
        return None
    return {"application/vnd.oasis.opendocument.text": "odt",
            "application/vnd.oasis.opendocument.spreadsheet": "ods",
            "application/vnd.oasis.opendocument.presentation": "odp"}.get(mt)


def _odf_meta(zf):
    try:
        root = safe_xml(read_part(zf, "meta.xml", 4 * 1024 * 1024))
    except ExtractError:
        return meta()
    vals = {}
    for el in root.iter():
        t = local(el.tag)
        if t in ("title", "creator", "initial-creator", "creation-date", "date") and el.text:
            vals.setdefault(t, el.text.strip())
    return meta(vals.get("title"), vals.get("creator") or vals.get("initial-creator"), vals.get("creation-date"),
                vals.get("date"))


def _inline_text(el):
    """Text of a text:p / text:h including spans, spaces, tabs and line breaks (frames are handled by the caller)."""
    parts = []

    def walk(e):
        if e.text and local(e.tag) not in ("annotation", "note"):
            parts.append(e.text)
        for ch in e:
            t = local(ch.tag)
            if t == "s":
                parts.append(" " * min(int(ch.get("{urn:oasis:names:tc:opendocument:xmlns:text:1.0}c", "1") or 1), 100))
            elif t == "tab":
                parts.append("\t")
            elif t == "line-break":
                parts.append("\n")
            elif t in ("annotation", "note", "frame"):
                pass
            else:
                walk(ch)
            if ch.tail:
                parts.append(ch.tail)
    walk(el)
    return clean("".join(parts))


def _odf_tables_in(el, lim):
    """Rows of one table:table element -> (rows, total_rows, widest)."""
    rows, total, widest = [], 0, 0
    for tr in el.iter(NS_TABLE + "table-row"):
        reps = min(int(tr.get(NS_TABLE + "number-rows-repeated", "1") or 1), MAX_REPEAT)
        cells = []
        for c in tr:
            if c.tag not in (NS_TABLE + "table-cell", NS_TABLE + "covered-table-cell"):
                continue
            creps = min(int(c.get(NS_TABLE + "number-columns-repeated", "1") or 1), MAX_REPEAT)
            vt = c.get(NS_OFFICE + "value-type")
            if vt in ("float", "percentage", "currency"):
                v = c.get(NS_OFFICE + "value")
                try:
                    text = cell_text(float(v))
                except (TypeError, ValueError):
                    text = _cell_paragraphs(c)
            elif vt == "date":
                text = (c.get(NS_OFFICE + "date-value") or "").replace("T00:00:00", "")
            elif vt == "boolean":
                text = "TRUE" if c.get(NS_OFFICE + "boolean-value") == "true" else "FALSE"
            else:
                text = _cell_paragraphs(c)
            cells.extend([text] * creps)
            if len(cells) > MAX_COLS * 4:
                break
        while cells and cells[-1] == "":
            cells.pop()
        if not cells and reps > 1:
            total += reps   # a run of empty rows (typically the sheet tail): counted, not materialised
            continue
        for _ in range(reps):
            total += 1
            if len(rows) < lim["max_rows"]:
                rows.append(cells[:MAX_COLS])
            widest = max(widest, len(cells))
    while rows and not any(rows[-1]):
        rows.pop()
    return rows, total, widest


def _cell_paragraphs(cell):
    out = []
    for p in cell.iter():
        if p.tag in (NS_TEXT + "p", NS_TEXT + "h"):
            t = _inline_text(p)
            if t:
                out.append(t)
    return " ".join(out)


def _walk_odf_body(el, blocks, lim, level=0):
    for ch in el:
        if len(blocks) >= lim["max_blocks"]:
            return
        tag = ch.tag
        if tag == NS_PRES + "notes":
            continue
        if tag == NS_TEXT + "h":
            t = _inline_text(ch).strip()
            if t:
                blocks.append(heading(t, int(ch.get(NS_TEXT + "outline-level", "1") or 1)))
        elif tag == NS_TEXT + "p":
            t = _inline_text(ch).strip()
            if t:
                blocks.append(paragraph(t))
            for fr in ch.iter(NS_DRAW + "frame"):
                _walk_odf_body(fr, blocks, lim, level)
        elif tag == NS_TEXT + "list":
            for item in ch.iter(NS_TEXT + "list-item"):
                for p in item:
                    if p.tag in (NS_TEXT + "p", NS_TEXT + "h"):
                        t = _inline_text(p).strip()
                        if t:
                            blocks.append(list_item(t, level))
        elif tag == NS_TABLE + "table":
            rows, _, _ = _odf_tables_in(ch, lim)
            if rows:
                blocks.append(table(rows))
        elif tag in (NS_TEXT + "section", NS_TEXT + "tracked-changes", NS_DRAW + "text-box", NS_DRAW + "frame",
                     NS_DRAW + "custom-shape"):
            if tag != NS_TEXT + "tracked-changes":
                _walk_odf_body(ch, blocks, lim, level)


def parse_odf(path, lim, expect=None):
    zf = open_zip_checked(path)
    try:
        kind = odf_kind(zf)
        if kind is None:
            raise ExtractError("UNREADABLE_FILE", "not an OpenDocument file")
        content = read_part(zf, "content.xml")
        manifest = b""
        try:
            manifest = read_part(zf, "META-INF/manifest.xml", 4 * 1024 * 1024)
        except ExtractError:
            pass
        if b"encryption-data" in manifest:
            raise ExtractError("ENCRYPTED", "password protected OpenDocument file")
        root = safe_xml(content)
        body = root.find(NS_OFFICE + "body")
        if body is None:
            raise ExtractError("UNREADABLE_FILE", "no document body")
        parsed_meta = _odf_meta(zf)
    finally:
        zf.close()
    if kind == "ods":
        parsed = new_parsed("spreadsheet")
        sheet_root = body.find(NS_OFFICE + "spreadsheet")
        hidden_styles = set()
        for st in root.iter(NS_STYLE + "style"):
            for tp in st.iter(NS_STYLE + "table-properties"):
                if tp.get(NS_TABLE + "display") == "false":
                    hidden_styles.add(st.get(NS_STYLE + "name"))
        for tbl in (sheet_root if sheet_root is not None else []):
            if tbl.tag != NS_TABLE + "table":
                continue
            rows, total, widest = _odf_tables_in(tbl, lim)
            name = tbl.get(NS_TABLE + "name") or "Sheet%d" % (len(parsed["sheets"]) + 1)
            if total > lim["max_rows"]:
                parsed["warnings"].append("ROW_CAP_REACHED:" + name)
            parsed["sheets"].append({"name": name, "rows": rows, "rows_total": total, "cols_total": widest,
                                     "hidden": tbl.get(NS_TABLE + "style-name") in hidden_styles})
    elif kind == "odp":
        parsed = new_parsed("presentation")
        pres = body.find(NS_OFFICE + "presentation")
        for page in (pres if pres is not None else []):
            if page.tag != NS_DRAW + "page":
                continue
            blocks = []
            notes = ""
            for ch in page:
                if ch.tag == NS_PRES + "notes":
                    parts = [_inline_text(p).strip() for p in ch.iter(NS_TEXT + "p")]
                    notes = "\n".join(t for t in parts if t)
            _walk_odf_body(page, blocks, lim)
            parsed["slides"].append({"blocks": blocks, "notes": notes, "hidden": False})
            if len(parsed["slides"]) >= 2000:
                parsed["warnings"].append("SLIDE_CAP_REACHED")
                break
    else:
        parsed = new_parsed("document")
        text = body.find(NS_OFFICE + "text")
        if text is not None:
            _walk_odf_body(text, parsed["blocks"], lim)
    parsed["metadata"] = parsed_meta
    return parsed


# ---------------------------------------------------------------------------------------------------- pptx

NS_A = "{http://schemas.openxmlformats.org/drawingml/2006/main}"
NS_P = "{http://schemas.openxmlformats.org/presentationml/2006/main}"
NS_R = "{http://schemas.openxmlformats.org/officeDocument/2006/relationships}"
NS_REL = "{http://schemas.openxmlformats.org/package/2006/relationships}"


def _a_paragraphs(el):
    out = []
    for p in el.iter(NS_A + "p"):
        parts = []
        for node in p.iter():
            t = node.tag
            if t == NS_A + "t" and node.text:
                parts.append(node.text)
            elif t == NS_A + "br":
                parts.append("\n")
            elif t == NS_A + "tab":
                parts.append("\t")
        text = clean("".join(parts)).strip()
        if text:
            out.append(text)
    return out


def _shape_blocks(tree, blocks):
    """Text shapes and tables of one slide in document order."""
    def walk(el):
        for ch in el:
            t = ch.tag
            if t == NS_P + "sp":
                ph = None
                for n in ch.iter(NS_P + "ph"):
                    ph = n.get("type") or "body"
                    break
                paras = _a_paragraphs(ch.find(NS_P + "txBody")) if ch.find(NS_P + "txBody") is not None else []
                if ph in ("sldNum", "dt", "ftr", "hdr") and not paras:
                    continue
                for i, text in enumerate(paras):
                    if ph in ("title", "ctrTitle") and i == 0:
                        blocks.append(heading(text.replace("\n", " "), 3))
                    else:
                        blocks.append(paragraph(text))
            elif t == NS_P + "graphicFrame":
                tbl = next(ch.iter(NS_A + "tbl"), None)
                if tbl is not None:
                    rows = []
                    for tr in tbl.iter(NS_A + "tr"):
                        rows.append([" ".join(_a_paragraphs(tc)) for tc in tr.iter(NS_A + "tc")])
                    if rows:
                        blocks.append(table(rows))
            elif t in (NS_P + "grpSp", NS_P + "spTree", NS_P + "cSld"):
                walk(ch)
    walk(tree)


def _rels(zf, name):
    try:
        root = safe_xml(read_part(zf, name, 4 * 1024 * 1024))
    except ExtractError:
        return {}
    return {r.get("Id"): (r.get("Target"), r.get("Type", "")) for r in root.iter(NS_REL + "Relationship")}


def _resolve(base_dir, target):
    import posixpath
    return posixpath.normpath(posixpath.join(base_dir, target)) if not target.startswith("/") else target.lstrip("/")


def parse_pptx(path, lim):
    zf = open_zip_checked(path)
    try:
        names = set(zf.namelist())
        if "ppt/presentation.xml" not in names:
            raise ExtractError("UNREADABLE_FILE", "not a pptx package")
        pres = safe_xml(read_part(zf, "ppt/presentation.xml"))
        rels = _rels(zf, "ppt/_rels/presentation.xml.rels")
        parsed = new_parsed("presentation")
        order = []
        for sld in pres.iter(NS_P + "sldId"):
            rid = sld.get(NS_R + "id")
            if rid in rels:
                order.append(_resolve("ppt", rels[rid][0]))
        if not order:
            order = sorted((n for n in names if re.fullmatch(r"ppt/slides/slide\d+\.xml", n)),
                           key=lambda n: int(re.search(r"(\d+)\.xml", n).group(1)))
        for slide_path in order[:2000]:
            if slide_path not in names:
                parsed["partial"] = True
                continue
            tree = safe_xml(read_part(zf, slide_path))
            blocks = []
            _shape_blocks(tree, blocks)
            notes = ""
            srels = _rels(zf, "ppt/slides/_rels/%s.rels" % slide_path.rsplit("/", 1)[-1])
            for target, typ in srels.values():
                if typ.endswith("/notesSlide"):
                    np = _resolve("ppt/slides", target)
                    if np in names:
                        ntree = safe_xml(read_part(zf, np))
                        lines = []
                        for sp in ntree.iter(NS_P + "sp"):
                            ph = next((n.get("type") for n in sp.iter(NS_P + "ph")), None)
                            if ph == "body":
                                lines.extend(_a_paragraphs(sp))
                        notes = "\n".join(lines)
            hidden = tree.get("show") == "0"
            parsed["slides"].append({"blocks": blocks, "notes": notes, "hidden": hidden})
        if len(order) > 2000:
            parsed["warnings"].append("SLIDE_CAP_REACHED")
        try:
            core = safe_xml(read_part(zf, "docProps/core.xml", 1024 * 1024))
            v = {local(e.tag): (e.text or "").strip() for e in core}
            parsed["metadata"] = meta(v.get("title"), v.get("creator"), v.get("created"), v.get("modified"))
        except ExtractError:
            pass
    finally:
        zf.close()
    return parsed


def sniff_zip(path):
    """-> 'docx' | 'xlsx' | 'pptx' | 'odt' | 'ods' | 'odp' | 'zip_other' | 'zip_broken'."""
    try:
        zf = zipfile.ZipFile(path)
    except zipfile.BadZipFile:
        return "zip_broken"
    except Exception:
        return "zip_broken"
    try:
        names = set(zf.namelist())
        if "mimetype" in names:
            k = odf_kind(zf)
            if k:
                return k
        if "[Content_Types].xml" in names or "word/document.xml" in names or "xl/workbook.xml" in names:
            if "word/document.xml" in names:
                return "docx"
            if "xl/workbook.xml" in names:
                return "xlsx"
            if "ppt/presentation.xml" in names:
                return "pptx"
        return "zip_other"
    except Exception:
        return "zip_broken"
    finally:
        zf.close()
