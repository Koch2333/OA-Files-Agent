"""Format coverage: Office 97-2003, OpenDocument, RTF, text/CSV/HTML/mail, content sniffing, robustness.
Fixtures live in tests/samples/formats (see formats-manifest.json for which are genuine Office output)."""
import io
import json
import os
import random
import struct
import subprocess
import sys
import tempfile
import unittest
import zipfile

import extract_core as ec
import fmt_common
import formats_data

HERE = os.path.dirname(os.path.abspath(__file__))
FX = os.path.normpath(os.path.join(HERE, "..", "..", "tests", "samples", "formats"))
CORE = os.path.join(HERE, "extract_core.py")


def fx(name):
    return os.path.join(FX, name)


def ext_of(name):
    return os.path.splitext(name)[1].lower()


def extract(name, ocr=False):
    return ec.extract(fx(name), ext_of(name), ocr)


def preview(name, **kw):
    return ec.preview(fx(name), ext_of(name), **kw)


def run_child(path, ext, timeout=60, mode=None):
    """Run the isolated extractor child exactly like the server does -> (returncode, parsed JSON or None)."""
    args = [sys.executable, "-I", CORE] + ([mode] if mode else []) + [path, ext] + (["0"] if mode is None else ["200", "600"])
    proc = subprocess.run(args, stdin=subprocess.DEVNULL, capture_output=True, timeout=timeout, cwd=HERE)
    try:
        return proc.returncode, json.loads(proc.stdout.decode("utf-8"))
    except ValueError:
        return proc.returncode, None


class TableTest(unittest.TestCase):
    def test_generated_copy_matches_the_contract_source(self):
        # contracts/formats.json is the single source; formats_data.py is generated from it (scripts/sync_formats.py)
        src = os.path.normpath(os.path.join(HERE, "..", "..", "contracts", "formats.json"))
        if not os.path.exists(src):
            self.skipTest("contracts/formats.json not available")
        with open(src, encoding="utf-8") as f:
            self.assertEqual(json.load(f), formats_data.TABLE)

    def test_every_extractor_in_the_table_has_a_reader(self):
        import sniff
        known = {"docx", "xlsx", "pdf", "image", "doc", "xls", "ppt", "msg", "odt", "ods", "odp", "pptx", "rtf", "html", "eml", "text", "csv", "ole"}
        self.assertEqual({f["extractor"] for f in formats_data.TABLE["formats"]} - known, set())
        self.assertEqual(set(sniff.EXTRACTOR_OF), ec.SUPPORTED_EXTS)

    def test_table_shape(self):
        exts = [f["ext"] for f in formats_data.TABLE["formats"]]
        self.assertEqual(len(exts), len(set(exts)))
        for needed in (".doc", ".xls", ".ppt", ".pptx", ".odt", ".rtf", ".csv", ".html", ".eml", ".webp", ".tiff"):
            self.assertIn(needed, exts)
        self.assertEqual(ec.SUPPORTED_EXTS, set(exts))


class WordBinaryTest(unittest.TestCase):
    def test_report(self):
        r = extract("word97_report.doc")
        t = r["text"]
        for needle in ("费用报销申请单", "申请部门：财务部", "Café naïve", "差旅费\t2\t1280.00", "办公用品\t10\t2000.00",
                       "第一项审批意见", "END-OF-DOC", "某某集团有限公司 内部文件", "Footer-Marker"):
            self.assertIn(needle, t)
        self.assertEqual((r["content_status"], r["detected_format"], r["extension_mismatch"]), ("ok", "doc", False))
        self.assertIsNone(r["quality"])
        self.assertEqual(set(r["metadata"]), {"title", "author", "created", "modified"})
        self.assertTrue(r["metadata"]["created"])

    def test_compressed_and_edited_pieces(self):
        self.assertEqual(extract("word97_latin.doc")["text"], "Plain ASCII document. Quarterly report Q1 2026.\nSecond paragraph with café.")
        t = extract("word97_edited.doc")["text"]
        self.assertTrue(t.startswith("Inserted first. Plain ASCII"))
        self.assertTrue(t.endswith("Appended later 追加内容"))

    def test_encrypted(self):
        with self.assertRaises(ec.ExtractError) as cm:
            extract("word97_encrypted.doc")
        self.assertEqual(cm.exception.code, "ENCRYPTED")

    def test_preview_structure(self):
        pv = preview("word97_report.doc")
        self.assertEqual(pv["kind"], "document")
        kinds = [b["type"] for b in pv["blocks"]]
        self.assertIn("table", kinds)
        table = next(b for b in pv["blocks"] if b["type"] == "table")
        self.assertEqual(table["rows"][0], ["项目", "数量", "金额"])
        self.assertIn("某某集团有限公司 内部文件", pv["header"])
        self.assertFalse(pv["truncated"])

    def test_preview_block_cap(self):
        pv = preview("word97_report.doc", max_blocks=2)
        self.assertEqual(len(pv["blocks"]), 2)
        self.assertTrue(pv["truncated"])


class ExcelBinaryTest(unittest.TestCase):
    def test_cells_dates_formulas_hidden(self):
        r = extract("excel97_purchase.xls")
        t = r["text"]
        self.assertIn("[采购明细]", t)
        self.assertIn("1\t设备1\t4\t201\t804\t2026-03-02\tFALSE", t)       # formula cached value, date, boolean
        self.assertIn("2\t设备2\t6\t301.5\t1809\t2026-03-03\tTRUE", t)
        self.assertIn("汇总\t\t\t\t18090", t)
        self.assertIn("合并单元格 merged", t)
        self.assertIn("#DIV/0!", t)
        self.assertIn("[隐藏表 (隐藏)]", t)
        self.assertTrue(any(w.startswith("HIDDEN_SHEETS:") for w in r["warnings"]))
        self.assertEqual(r["content_status"], "ok")

    def test_preview_hides_hidden_sheet(self):
        pv = preview("excel97_purchase.xls")
        self.assertEqual(pv["kind"], "spreadsheet")
        self.assertEqual([s["name"] for s in pv["sheets"]], ["采购明细", "Sheet 2"])
        self.assertEqual(pv["sheets"][0]["rows"][0][:3], ["序号", "名称", "数量"])

    def test_preview_caps(self):
        pv = preview("excel97_purchase.xls", max_rows=3)
        self.assertEqual(len(pv["sheets"][0]["rows"]), 3)
        self.assertTrue(pv["sheets"][0]["truncated"])
        self.assertTrue(pv["truncated"])

    def test_encrypted(self):
        with self.assertRaises(ec.ExtractError) as cm:
            extract("excel97_encrypted.xls")
        self.assertEqual(cm.exception.code, "ENCRYPTED")


class PowerPointTest(unittest.TestCase):
    def check(self, name):
        r = extract(name)
        self.assertEqual(len(r["pages"]), 2)
        p1, p2 = r["pages"][0]["text"], r["pages"][1]["text"]
        for needle in ("2026 年度工作汇报", "第二点：Budget in USD", "speaker-notes-marker"):
            self.assertIn(needle, p1)
        for needle in ("Textbox-Marker", "列A", "20"):
            self.assertIn(needle, p2)
        self.assertEqual(r["content_status"], "ok")

    def test_ppt(self):
        self.check("ppt97_deck.ppt")

    def test_pptx(self):
        self.check("deck.pptx")

    def test_odp(self):
        self.check("deck.odp")

    def test_ppt_encrypted(self):
        with self.assertRaises(ec.ExtractError) as cm:
            extract("ppt97_encrypted.ppt")
        self.assertEqual(cm.exception.code, "ENCRYPTED")

    def test_preview_has_slide_headings(self):
        pv = preview("ppt97_deck.ppt")
        heads = [b["text"] for b in pv["blocks"] if b["type"] == "heading" and b["level"] == 2]
        self.assertEqual(heads, ["幻灯片 1", "幻灯片 2"])


class OpenDocumentAndRtfTest(unittest.TestCase):
    def test_odt_rtf_html_agree(self):
        for name in ("word_report.odt", "word_report.rtf", "word_report.html"):
            t = extract(name)["text"]
            for needle in ("费用报销申请单", "申请部门：财务部", "差旅费\t2\t1280.00", "第一项审批意见", "END-OF-DOC"):
                self.assertIn(needle, t, name)
            self.assertNotIn("Wingdings", t, name)
            self.assertNotIn("panose", t, name)

    def test_ods(self):
        r = extract("excel_purchase.ods")
        self.assertIn("1\t设备1\t4\t201\t804\t2026-03-02\tFALSE", r["text"])
        self.assertEqual(preview("excel_purchase.ods")["kind"], "spreadsheet")

    def test_rtf_unicode_and_hex(self):
        import text_formats as tf
        lim = {"max_rows": 10, "max_blocks": 100}
        p = tf.rtf_to_parsed(br"{\rtf1\ansi\ansicpg936\uc1{\fonttbl{\f0\fcharset134 x;}}\f0 \'b5\'c8 \u20013?\par b\u233?c\par}", lim)
        self.assertEqual(fmt_common.blocks_text(p["blocks"]), "等 中\nbéc")

    def test_rtf_skips_binary_destinations(self):
        import text_formats as tf
        p = tf.rtf_to_parsed(br"{\rtf1 visible{\*\generator Hidden Gen;}{\pict\jpegblip 0102ff}{\object{\*\objdata 0105}}\par}",
                             {"max_rows": 10, "max_blocks": 100})
        self.assertEqual(fmt_common.blocks_text(p["blocks"]), "visible")

    def test_html_visible_text_only(self):
        t = extract("page.html")["text"]
        self.assertIn("放假通知", t)
        self.assertIn("发文部门：行政部\n日期：2026-09-28", t)
        for hidden in ("X-SCRIPT-TEXT", "NOSCRIPT-TEXT", "p{}"):
            self.assertNotIn(hidden, t)
        self.assertEqual(extract("page.html")["metadata"]["title"], "通知")


class TextFormatsTest(unittest.TestCase):
    EXPECT = "关于2026年国庆节放假安排的通知\n发文部门：行政部\n发文日期：2026-09-28"

    def test_encodings(self):
        for name, enc in (("text_utf8.txt", "utf-8"), ("text_utf8_bom.txt", "utf-8-sig"), ("text_utf16.txt", "utf-16"),
                          ("text_gbk.txt", "gb18030"), ("text_gb18030.txt", "gb18030")):
            r = extract(name)
            self.assertTrue(r["text"].startswith(self.EXPECT), name)
            self.assertIn("ENCODING:" + enc, r["warnings"], name)
            self.assertEqual(r["content_status"], "ok", name)

    def test_low_confidence_fallback(self):
        import text_formats as tf
        # cp1252 text that is not valid UTF-8 and has no CJK structure: flagged, never silently trusted
        text, enc, low = tf.decode_text("caf\xe9 cr\xe8me br\xfbl\xe9e".encode("cp1252"))
        self.assertTrue(low)
        self.assertEqual(enc, "cp1252")
        self.assertIn("café", text)

    def test_markdown_blocks(self):
        pv = preview("notes.md")
        kinds = [(b["type"], b.get("level")) for b in pv["blocks"]]
        self.assertEqual(kinds[0], ("heading", 1))
        self.assertIn(("list_item", 0), kinds)

    def test_csv_tsv(self):
        for name in ("data_utf8.csv", "data_gbk.csv", "data.tsv"):
            r = extract(name)
            self.assertIn("笔记本电脑\t20\t6800\t联想科技", r["text"], name)
            pv = preview(name)
            self.assertEqual(pv["kind"], "spreadsheet")
            self.assertEqual(pv["sheets"][0]["rows"][0], ["设备名称", "数量", "单价", "供应商"])

    def test_csv_row_cap(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "big.csv")
            with open(p, "w") as f:
                f.write("a,b\n" + "1,2\n" * 7000)
            r = ec.extract(p, ".csv", False)
            self.assertTrue(any(w.startswith("ROW_CAP_REACHED") for w in r["warnings"]))
            self.assertEqual(r["content_status"], "partial")
            pv = ec.preview(p, ".csv", max_rows=50)
            self.assertEqual(len(pv["sheets"][0]["rows"]), 50)
            self.assertEqual(pv["sheets"][0]["rows_total"], 7001)

    def test_eml_and_msg(self):
        r = extract("mail.eml")
        for needle in ("主题：设备采购审批", "发件人：张伟 <zhang@example.com>", "您好，附件为设备采购审批单", "附件：approval.xlsx"):
            self.assertIn(needle, r["text"])
        self.assertEqual(r["metadata"]["title"], "设备采购审批")
        m = extract("mail.msg")
        for needle in ("主题：采购部_设备采购审批", "发件人：李明", "请审批"):
            self.assertIn(needle, m["text"])


class SniffTest(unittest.TestCase):
    def test_disguised(self):
        cases = {"disguised_rtf.doc": "rtf", "disguised_docx.doc": "docx", "disguised_pdf.txt": "pdf",
                 "disguised_doc.docx": "doc", "web_export.xls": "html"}
        for name, really in cases.items():
            r = extract(name)
            self.assertEqual((r["detected_format"], r["extension_mismatch"]), (really, True), name)
            self.assertIn("FORMAT_MISMATCH:" + really, r["warnings"], name)
            self.assertTrue(r["text"].strip(), name)

    def test_web_export_xls_becomes_sheet(self):
        pv = preview("web_export.xls")
        self.assertEqual(pv["kind"], "spreadsheet")
        self.assertEqual(pv["sheets"][0]["rows"], [["设备名称", "数量"], ["笔记本电脑", "20"]])
        self.assertTrue(any("扩展名与内容不符" in w for w in pv["warnings"]))

    def test_no_mismatch_for_honest_files(self):
        for name in ("word97_report.doc", "excel97_purchase.xls", "deck.pptx", "text_utf8.txt", "data_utf8.csv", "page.html"):
            self.assertFalse(extract(name)["extension_mismatch"], name)

    def test_unknown_extension(self):
        with tempfile.TemporaryDirectory() as d:
            data = open(fx("word97_report.doc"), "rb").read()
            p = os.path.join(d, "report.dat")
            open(p, "wb").write(data)
            r = ec.extract(p, ".dat", False)           # recognised binary content is read even without a known extension
            self.assertEqual((r["detected_format"], r["extension_mismatch"]), ("doc", True))
            q = os.path.join(d, "notes.json")
            open(q, "w").write('{"a": 1}')
            with self.assertRaises(ec.ExtractError) as cm:    # plain text under an unknown extension stays unsupported
                ec.extract(q, ".json", False)
            self.assertEqual(cm.exception.code, "UNSUPPORTED_FORMAT")

    def test_unknown_binary_and_empty(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "x.bin")
            open(p, "wb").write(os.urandom(2048) + b"\x00\x01")
            with self.assertRaises(ec.ExtractError) as cm:
                ec.extract(p, ".bin", False)
            self.assertEqual(cm.exception.code, "UNSUPPORTED_FORMAT")
            with self.assertRaises(ec.ExtractError) as cm:     # same bytes under an Office name: damaged, not unsupported
                ec.extract(p, ".doc", False)
            self.assertEqual(cm.exception.code, "UNREADABLE_FILE")
            e = os.path.join(d, "empty.docx")
            open(e, "wb").close()
            with self.assertRaises(ec.ExtractError) as cm:
                ec.extract(e, ".docx", False)
            self.assertEqual(cm.exception.code, "UNREADABLE_FILE")

    def test_zip_archives_are_not_documents(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "a.zip")
            with zipfile.ZipFile(p, "w") as z:
                z.writestr("hello.txt", "hi")
            with self.assertRaises(ec.ExtractError) as cm:
                ec.extract(p, ".zip", False)
            self.assertEqual(cm.exception.code, "UNSUPPORTED_FORMAT")

    def test_wps_extensions_follow_content(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "a.wps")
            open(p, "wb").write(open(fx("word97_report.doc"), "rb").read())
            self.assertIn("费用报销申请单", ec.extract(p, ".wps", False)["text"])
            q = os.path.join(d, "b.wps")
            open(q, "wb").write(b"\x00\x01WPS native proprietary" + os.urandom(100))
            with self.assertRaises(ec.ExtractError) as cm:
                ec.extract(q, ".wps", False)
            self.assertIn(cm.exception.code, ("UNSUPPORTED_FORMAT", "UNREADABLE_FILE"))


class RobustnessTest(unittest.TestCase):
    """Hostile or damaged input must give a structured error or a (possibly partial) result - never a crash, hang or
    unbounded allocation. Run in-process for speed; the child-process tests below cover crashes and timeouts."""

    BINARY = ["word97_report.doc", "word97_latin.doc", "excel97_purchase.xls", "ppt97_deck.ppt", "mail.msg", "deck.pptx",
              "word_report.odt", "excel_purchase.ods", "deck.odp", "word_report.rtf"]

    def attempt(self, path, ext):
        try:
            r = ec.extract(path, ext, False)
            self.assertIn(r["content_status"], ("ok", "partial", "unreadable"))
            ec.preview(path, ext)
        except ec.ExtractError as e:
            self.assertIn(e.code, ("UNREADABLE_FILE", "ENCRYPTED", "UNSUPPORTED_FORMAT", "INVALID_REQUEST"))

    def test_truncations(self):
        with tempfile.TemporaryDirectory() as d:
            for name in self.BINARY:
                data = open(fx(name), "rb").read()
                for frac in (0.02, 0.1, 0.33, 0.5, 0.75, 0.97):
                    p = os.path.join(d, "t" + ext_of(name))
                    open(p, "wb").write(data[:max(1, int(len(data) * frac))])
                    self.attempt(p, ext_of(name))

    def test_random_bytes_and_bit_flips(self):
        rnd = random.Random(1234)
        with tempfile.TemporaryDirectory() as d:
            for name in self.BINARY:
                data = bytearray(open(fx(name), "rb").read())
                for trial in range(12):
                    mutated = bytearray(data)
                    for _ in range(rnd.randint(1, 40)):
                        mutated[rnd.randrange(len(mutated))] = rnd.randrange(256)
                    p = os.path.join(d, "m" + ext_of(name))
                    open(p, "wb").write(bytes(mutated))
                    self.attempt(p, ext_of(name))
            for ext in (".doc", ".xls", ".ppt", ".docx", ".odt", ".rtf", ".msg", ".csv", ".html", ".eml", ".png"):
                p = os.path.join(d, "r" + ext)
                open(p, "wb").write(os.urandom(5000))
                self.attempt(p, ext)

    def test_ole_header_with_garbage(self):
        with tempfile.TemporaryDirectory() as d:
            for ext in (".doc", ".xls", ".ppt", ".msg"):
                p = os.path.join(d, "g" + ext)
                open(p, "wb").write(b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1" + os.urandom(4000))
                self.attempt(p, ext)

    def patch_doc(self, mutate):
        data = bytearray(open(fx("word97_latin.doc"), "rb").read())
        mutate(data)
        return bytes(data)

    def test_ole_declared_size_bomb(self):
        # set the declared size of every directory entry to 2 GiB: reading must be refused, not allocated
        def mutate(data):
            for off in range(0, len(data) - 128, 128):
                name_len = struct.unpack_from("<H", data, off + 64)[0]
                if 2 <= name_len <= 64 and data[off + 66] == 2:
                    struct.pack_into("<Q", data, off + 120, 0x7FFFFFFF)
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "bomb.doc")
            open(p, "wb").write(self.patch_doc(mutate))
            with self.assertRaises(ec.ExtractError) as cm:
                ec.extract(p, ".doc", False)
            self.assertEqual(cm.exception.code, "UNREADABLE_FILE")

    def test_cyclic_fat_chain_in_child(self):
        # point every FAT entry at itself: olefile must report a loop; the child must exit cleanly and quickly
        data = bytearray(open(fx("word97_latin.doc"), "rb").read())
        fat_sect = struct.unpack_from("<I", data, 76)[0]
        base = 512 + fat_sect * 512
        for i in range(128):
            struct.pack_into("<I", data, base + 4 * i, i)
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "loop.doc")
            open(p, "wb").write(bytes(data))
            code, body = run_child(p, ".doc", timeout=30)
            self.assertEqual(code, 0)
            self.assertTrue("data" in body or body["error"]["code"] in ("UNREADABLE_FILE", "UNSUPPORTED_FORMAT"))

    def test_difat_loop_in_child(self):
        data = bytearray(open(fx("word97_latin.doc"), "rb").read())
        struct.pack_into("<I", data, 68, 1)                 # DIFAT start sector = 1, count = 1 (chain of garbage)
        struct.pack_into("<I", data, 72, 0xFFFFFF)
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "difat.doc")
            open(p, "wb").write(bytes(data))
            code, body = run_child(p, ".doc", timeout=30)
            self.assertEqual(code, 0)
            self.assertIn("error", body)

    def test_zip_bomb(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "bomb.docx")
            with zipfile.ZipFile(p, "w", zipfile.ZIP_DEFLATED) as z:
                z.writestr("[Content_Types].xml", "<Types/>")
                z.writestr("word/document.xml", b"\x00" * (300 * 1024 * 1024))
            self.assertLess(os.path.getsize(p), 2 * 1024 * 1024)
            with self.assertRaises(ec.ExtractError) as cm:
                ec.extract(p, ".docx", False)
            self.assertEqual(cm.exception.code, "UNREADABLE_FILE")
            q = os.path.join(d, "bomb.odt")
            with zipfile.ZipFile(q, "w", zipfile.ZIP_DEFLATED) as z:
                z.writestr("mimetype", "application/vnd.oasis.opendocument.text", compress_type=zipfile.ZIP_STORED)
                z.writestr("content.xml", b"<a>" + b"x" * (300 * 1024 * 1024) + b"</a>")
            with self.assertRaises(ec.ExtractError) as cm:
                ec.extract(q, ".odt", False)
            self.assertEqual(cm.exception.code, "UNREADABLE_FILE")

    def test_xml_entity_expansion_refused(self):
        laughs = (b'<?xml version="1.0"?><!DOCTYPE lolz [<!ENTITY lol "lol"><!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;">]>'
                  b'<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0">&lol2;'
                  b'</office:document-content>')
        with tempfile.TemporaryDirectory() as d:
            q = os.path.join(d, "xxe.odt")
            with zipfile.ZipFile(q, "w") as z:
                z.writestr("mimetype", "application/vnd.oasis.opendocument.text", compress_type=zipfile.ZIP_STORED)
                z.writestr("content.xml", laughs)
            with self.assertRaises(ec.ExtractError) as cm:
                ec.extract(q, ".odt", False)
            self.assertEqual(cm.exception.code, "UNREADABLE_FILE")

    def test_rtf_nesting_and_huge_groups(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "deep.rtf")
            open(p, "wb").write(b"{\\rtf1 " + b"{" * 200000 + b"deep" + b"}" * 200000 + b"}")
            self.attempt(p, ".rtf")
            q = os.path.join(d, "wide.html")
            open(q, "wb").write(b"<html><body>" + b"<div>" * 100000 + b"x" + b"</div>" * 100000 + b"</body></html>")
            self.attempt(q, ".html")

    def test_crash_in_reader_is_contained(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "x.doc")
            open(p, "wb").write(open(fx("word97_report.doc"), "rb").read())
            original = ec._parsers
            ec._parsers = lambda: {"doc": lambda path, lim: (_ for _ in ()).throw(KeyError("boom"))}
            try:
                with self.assertRaises(ec.ExtractError) as cm:
                    ec.extract(p, ".doc", False)
                self.assertEqual(cm.exception.code, "UNREADABLE_FILE")
            finally:
                ec._parsers = original

    def test_child_never_crashes_on_every_fixture(self):
        for name in sorted(os.listdir(FX)):
            if name.endswith(".json"):
                continue
            code, body = run_child(fx(name), ext_of(name))
            self.assertEqual(code, 0, name)
            self.assertIn("data" if not ("encrypted" in name) else "error", body, name)


class PartialDocTest(unittest.TestCase):
    def test_damaged_table_stream_falls_back_to_partial_text(self):
        # wipe the whole 1Table stream (piece table lost): text must still come out, flagged partial
        import olefile
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "dmg.doc")
            data = open(fx("word97_latin.doc"), "rb").read()
            open(p, "wb").write(data)
            ole = olefile.OleFileIO(p, write_mode=True)
            try:
                name = "1Table" if ole.exists("1Table") else "0Table"
                size = ole.get_size(name)
                ole.write_stream(name, b"\x00" * size)
            finally:
                ole.close()
            r = ec.extract(p, ".doc", False)
            self.assertIn("Plain ASCII document", r["text"])
            self.assertEqual(r["content_status"], "partial")
            self.assertIn("LEGACY_PARTIAL", r["warnings"])


if __name__ == "__main__":
    unittest.main()
