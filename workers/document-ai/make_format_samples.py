"""Generate the script-built fixtures of tests/samples/formats/ (text encodings, CSV/TSV, web-export .xls, disguised
files, e-mail, extra image formats, a SYNTHESISED .msg) and write formats-manifest.json (with provenance).

The Word/Excel/PowerPoint 97-2003, ODF, RTF, HTML and password-protected fixtures in the same folder come from
make_office_fixtures.ps1 (genuine Microsoft Office via COM); this script only records them in the manifest.
Run with the worker venv:  python make_format_samples.py
"""
import datetime
import io
import json
import os
import shutil
import struct

from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.normpath(os.path.join(HERE, "..", "..", "tests", "samples", "formats"))
FONT = os.environ.get("OA_CJK_FONT", r"C:\Windows\Fonts\msyh.ttc")
ENTRIES = []

TEXT = ["关于2026年国庆节放假安排的通知", "发文部门：行政部", "发文日期：2026-09-28", "各部门：放假调休，详见附件。"]
CSV_ROWS = [["设备名称", "数量", "单价", "供应商"], ["笔记本电脑", "20", "6800", "联想科技"], ["显示器", "30", "1200", "戴尔中国"]]


def path(name):
    return os.path.join(OUT, name)


def record(name, provenance, detail, **extra):
    entry = {"file": name, "provenance": provenance, "detail": detail}
    entry.update(extra)
    ENTRIES.append(entry)


def write(name, data, provenance, detail, **extra):
    with open(path(name), "wb") as f:
        f.write(data)
    record(name, provenance, detail, **extra)


# ---- minimal OLE2 (CFB v3) writer: only to SYNTHESISE an Outlook .msg (olefile cannot create files) ---------------

def build_cfb(streams):
    """streams: {name: bytes} (flat, all in the root storage; each >= 4096 bytes is padded so no mini-stream is needed)."""
    SECT = 512
    ENDOFCHAIN, FREESECT, FATSECT, NOSTREAM = 0xFFFFFFFE, 0xFFFFFFFF, 0xFFFFFFFD, 0xFFFFFFFF
    items = []
    for name, data in streams.items():
        data = bytes(data)
        if len(data) < 4096:
            data = data + b"\x00" * (4096 - len(data))   # keep everything in regular sectors (size field stays honest below)
        items.append((name, data))
    # sector layout: [FAT sectors][directory sectors][stream sectors...]
    dir_entries = 1 + len(items)
    dir_sectors = (dir_entries + 3) // 4
    stream_sectors = [(len(d) + SECT - 1) // SECT for _, d in items]
    total_data = dir_sectors + sum(stream_sectors)
    fat_sectors = 1
    while fat_sectors * 128 < total_data + fat_sectors:
        fat_sectors += 1
    total = fat_sectors + total_data
    fat = [FREESECT] * (fat_sectors * 128)
    for i in range(fat_sectors):
        fat[i] = FATSECT
    nxt = fat_sectors
    dir_start = nxt
    for i in range(dir_sectors):
        fat[nxt + i] = nxt + i + 1 if i < dir_sectors - 1 else ENDOFCHAIN
    nxt += dir_sectors
    starts = []
    for (name, d), n in zip(items, stream_sectors):
        starts.append(nxt)
        for i in range(n):
            fat[nxt + i] = nxt + i + 1 if i < n - 1 else ENDOFCHAIN
        nxt += n
    hdr = bytearray(512)
    hdr[0:8] = b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"
    struct.pack_into("<HHHHHH", hdr, 24, 0x3E, 3, 0xFFFE, 9, 6, 0)
    struct.pack_into("<IIIIIIIII", hdr, 40, 0, fat_sectors, dir_start, 0, 4096, ENDOFCHAIN, 0, ENDOFCHAIN, 0)
    for i in range(109):
        struct.pack_into("<I", hdr, 76 + 4 * i, i if i < fat_sectors else FREESECT)

    def dirent(name, typ, left, right, child, start, size):
        e = bytearray(128)
        raw = name.encode("utf-16-le")
        e[0:len(raw)] = raw
        struct.pack_into("<HBB", e, 64, len(raw) + 2, typ, 1)
        struct.pack_into("<III", e, 68, left, right, child)
        struct.pack_into("<I", e, 116, start if start is not None else ENDOFCHAIN)
        struct.pack_into("<Q", e, 120, size)
        return bytes(e)
    entries = [dirent("Root Entry", 5, NOSTREAM, NOSTREAM, 1 if items else NOSTREAM, ENDOFCHAIN, 0)]
    for i, (name, d) in enumerate(items):
        # simple right-leaning sibling chain (valid, just unbalanced)
        entries.append(dirent(name, 2, NOSTREAM, i + 2 if i + 1 < len(items) else NOSTREAM, NOSTREAM, starts[i], len(d)))
    while len(entries) % 4:
        entries.append(bytes(128))
    body = bytearray()
    body += b"".join(struct.pack("<I", v) for v in fat)
    body += b"".join(entries)
    for _, d in items:
        body += d + b"\x00" * (-len(d) % SECT)
    return bytes(hdr) + bytes(body)


def make_msg():
    def u(s):
        return s.encode("utf-16-le")
    props = {
        "__substg1.0_0037001F": u("采购部_设备采购审批"),            # subject
        "__substg1.0_0C1A001F": u("李明"),                           # sender name
        "__substg1.0_0E04001F": u("张伟"),                           # display to
        "__substg1.0_1000001F": u("您好，附件为2026年3月设备采购审批单。\r\n请审批。"),   # body
        "__properties_version1.0": b"\x00" * 32,
    }
    return build_cfb(props)


def render_page(lines, size=(720, 440), font_size=32):
    img = Image.new("RGB", size, "white")
    draw = ImageDraw.Draw(img)
    title_font = ImageFont.truetype(FONT, int(font_size * 1.5))
    body_font = ImageFont.truetype(FONT, font_size)
    y = 50
    for i, line in enumerate(lines):
        draw.text((40, y), line, fill="black", font=title_font if i == 0 else body_font)
        y += int(font_size * (2.4 if i == 0 else 1.8))
    return img


def main():
    os.makedirs(OUT, exist_ok=True)
    body = "\n".join(TEXT) + "\n"
    write("text_utf8.txt", body.encode("utf-8"), "generated", "UTF-8 without BOM")
    write("text_utf8_bom.txt", b"\xef\xbb\xbf" + body.encode("utf-8"), "generated", "UTF-8 with BOM")
    write("text_utf16.txt", b"\xff\xfe" + body.encode("utf-16-le"), "generated", "UTF-16 LE with BOM")
    write("text_gbk.txt", body.encode("gbk"), "generated", "GBK, no BOM (typical old Chinese export)")
    write("text_gb18030.txt", body.encode("gb18030"), "generated", "GB18030, no BOM")
    write("notes.md", ("# 会议纪要\n\n- 事项一：采购申请\n- 事项二：预算\n\n正文段落。\n").encode("utf-8"), "generated", "Markdown")
    csv_text = "\r\n".join(",".join(r) for r in CSV_ROWS) + "\r\n"
    write("data_utf8.csv", csv_text.encode("utf-8-sig"), "generated", "CSV UTF-8 BOM")
    write("data_gbk.csv", csv_text.encode("gbk"), "generated", "CSV GBK (Excel on Chinese Windows)")
    write("data.tsv", ("\r\n".join("\t".join(r) for r in CSV_ROWS) + "\r\n").encode("utf-8"), "generated", "TSV")
    html_table = ("<html><head><meta charset=\"utf-8\"><title>采购汇总</title><style>td{color:red}</style></head><body>"
                  "<script>alert('never executed')</script><table><tr><td>设备名称</td><td>数量</td></tr>"
                  "<tr><td>笔记本电脑</td><td>20</td></tr></table></body></html>")
    write("web_export.xls", html_table.encode("utf-8"), "generated", "HTML table saved with an .xls extension (web system export)",
          really="html", extension_mismatch=True)
    write("page.html", ("<!DOCTYPE html><html><head><title>通知</title><style>p{}</style><script>var secret='X-SCRIPT-TEXT';</script></head>"
                        "<body><h1>放假通知</h1><p>发文部门：行政部<br>日期：2026-09-28</p><ul><li>第一项</li><li>第二项</li></ul>"
                        "<noscript>NOSCRIPT-TEXT</noscript></body></html>").encode("utf-8"), "generated", "HTML with script/style")
    with open(path("word_report.rtf"), "rb") as f:
        rtf = f.read()
    write("disguised_rtf.doc", rtf, "generated", "RTF content saved as .doc", really="rtf", extension_mismatch=True)
    with open(os.path.join(HERE, "..", "..", "tests", "samples", "财务部_2026年3月报销申请.docx"), "rb") as f:
        docx = f.read()
    write("disguised_docx.doc", docx, "generated", "DOCX content saved as .doc (common OA export)", really="docx", extension_mismatch=True)
    import pymupdf as fitz
    doc = fitz.open()
    doc.new_page().insert_text((72, 100), "Procurement contract 2026-03-15 - Purchasing Department")
    write("disguised_pdf.txt", doc.tobytes(), "generated", "small PDF (Latin text) saved as .txt", really="pdf", extension_mismatch=True)
    with open(path("word97_report.doc"), "rb") as f:
        doc97 = f.read()
    write("disguised_doc.docx", doc97, "generated", "Word 97-2003 content saved as .docx", really="doc", extension_mismatch=True)
    eml = ("From: =?utf-8?b?5byg5Lyf?= <zhang@example.com>\r\nTo: li@example.com\r\nSubject: =?utf-8?b?6K6+5aSH6YeH6LSt5a6h5om5?=\r\n"
           "Date: Fri, 27 Mar 2026 10:00:00 +0800\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n"
           "--b1\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n"
           + __import__("base64").b64encode("您好，附件为设备采购审批单，请审批。\n采购部".encode("utf-8")).decode() +
           "\r\n--b1\r\nContent-Type: application/octet-stream; name=\"approval.xlsx\"\r\nContent-Disposition: attachment; filename=\"approval.xlsx\"\r\n"
           "Content-Transfer-Encoding: base64\r\n\r\nUEsDBA==\r\n--b1--\r\n")
    write("mail.eml", eml.encode("ascii"), "generated", "multipart e-mail with a UTF-8 base64 body and one (unparsed) attachment")
    write("mail.msg", make_msg(), "SYNTHESISED", "hand-built OLE2 container with the Outlook property streams read by the parser; NOT produced by Outlook")
    img = render_page(["采购申请单", "申请部门：采购部", "物品：打印机 5台"])
    img.convert("L").save(path("scan.bmp"), "BMP")      # 8-bit grayscale keeps the fixture small
    record("scan.bmp", "generated", "BMP rendered with Pillow")
    img.save(path("scan.webp"), "WEBP", quality=90)
    record("scan.webp", "generated", "WebP rendered with Pillow")
    img.save(path("scan.gif"), "GIF")
    record("scan.gif", "generated", "GIF (single frame)")
    img.save(path("scan.jfif"), "JPEG", quality=90)
    record("scan.jfif", "generated", "JPEG saved as .jfif")
    page2 = render_page(["第二页", "发文部门：财务部", "日期：2026-05-20"])
    img.save(path("scan_2pages.tif"), "TIFF", save_all=True, append_images=[page2], compression="tiff_lzw")
    record("scan_2pages.tif", "generated", "two-page TIFF")
    office = {
        "word97_report.doc": "Word 97-2003 (nFib 0x0112 file written by Word 16 'Word 97-2003 Document')",
        "word97_latin.doc": "Word 97-2003, ASCII/cp1252 only (compressed pieces)",
        "word97_edited.doc": "Word 97-2003 re-opened and edited (several pieces)",
        "word97_encrypted.doc": "Word 97-2003 with an open password (RC4/CryptoAPI)",
        "excel97_purchase.xls": "BIFF8: 3 sheets (one hidden), dates, formulas, merged cells, error cell",
        "excel97_encrypted.xls": "BIFF8 with an open password",
        "ppt97_deck.ppt": "PowerPoint 97-2003: 2 slides, notes, textbox, table",
        "ppt97_encrypted.ppt": "PowerPoint 97-2003 with an open password",
        "word_report.rtf": "RTF written by Word (Chinese, code page 936, table, list)",
        "word_report.odt": "OpenDocument text written by Word",
        "word_report.html": "Filtered HTML written by Word (GB2312)",
        "excel_purchase.ods": "OpenDocument spreadsheet written by Excel",
        "deck.odp": "OpenDocument presentation written by PowerPoint",
        "deck.pptx": "PPTX written by PowerPoint",
    }
    for name, detail in office.items():
        if os.path.exists(path(name)):
            record(name, "office-produced", "Microsoft Office 16 via COM (make_office_fixtures.ps1): " + detail,
                   expected_error=("ENCRYPTED" if "encrypted" in name else None))
    manifest = {"generated": datetime.date.today().isoformat(),
                "note": "Provenance: office-produced = saved by genuine Microsoft Office; generated = built by script/library; "
                        "SYNTHESISED = hand-built, never opened by the real application. No fixture came from a third-party "
                        "real-world document.",
                "files": ENTRIES}
    with open(path("formats-manifest.json"), "w", encoding="utf-8") as f:
        json.dump(manifest, f, ensure_ascii=False, indent=2)
    print("wrote", len(ENTRIES), "entries")


if __name__ == "__main__":
    main()
