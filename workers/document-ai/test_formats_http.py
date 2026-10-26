"""HTTP-level checks for the additional formats (extract / render / preview through the real server and child process).
OCR-based cases are slow (the OCR models load per child), so they are kept to one file per image format."""
import os
import shutil
import tempfile
import unittest

import server
from test_server import SAMPLES, ServerCase

FX = os.path.join(SAMPLES, "formats")


class FormatsHttpTest(ServerCase):
    spool = FX

    def render(self, name, **extra):
        return self.call("POST", "/v1/render", dict({"path": os.path.join(FX, name)}, **extra))

    def preview(self, name, **extra):
        return self.call("POST", "/v1/preview", dict({"path": os.path.join(FX, name)}, **extra))

    def test_office_97_2003_extract_and_preview(self):
        for name, needle, kind in (("word97_report.doc", "费用报销申请单", "document"), ("excel97_purchase.xls", "采购明细", "spreadsheet"),
                                   ("ppt97_deck.ppt", "年度工作汇报", "document"), ("mail.eml", "设备采购审批", "document"),
                                   ("data_gbk.csv", "联想科技", "spreadsheet"), ("web_export.xls", "笔记本电脑", "spreadsheet")):
            status, body = self.extract(os.path.join(FX, name), ocr=False)
            self.assertEqual(status, 200, name)
            self.assertIn(needle, body["data"]["text"], name)
            self.assertEqual(body["data"]["content_status"], "ok", name)
            self.assertIn("detected_format", body["data"])
            status, body = self.preview(name)
            self.assertEqual((status, body["data"]["kind"]), (200, kind), name)

    def test_mismatch_is_reported(self):
        status, body = self.extract(os.path.join(FX, "disguised_rtf.doc"), ocr=False)
        self.assertEqual(status, 200)
        self.assertTrue(body["data"]["extension_mismatch"])
        self.assertEqual(body["data"]["detected_format"], "rtf")
        self.assertIn("FORMAT_MISMATCH:rtf", body["data"]["warnings"])

    def test_encrypted_files_are_structured_errors(self):
        for name in ("word97_encrypted.doc", "excel97_encrypted.xls", "ppt97_encrypted.ppt"):
            status, body = self.extract(os.path.join(FX, name), ocr=False)
            self.assertEqual((status, body["error"]["code"]), (422, "ENCRYPTED"), name)
            status, body = self.preview(name)
            self.assertEqual((status, body["error"]["code"]), (422, "ENCRYPTED"), name)

    def test_too_large_is_a_structured_error(self):
        old = server.MAX_FILE_BYTES
        server.MAX_FILE_BYTES = 1000
        try:
            status, body = self.extract(os.path.join(FX, "word97_report.doc"), ocr=False)
            self.assertEqual((status, body["error"]["code"]), (413, "FILE_TOO_LARGE"))
        finally:
            server.MAX_FILE_BYTES = old

    def test_unknown_and_empty(self):
        tmp = tempfile.mkdtemp(dir=FX)
        try:
            junk = os.path.join(tmp, "x.dwg")
            with open(junk, "wb") as f:
                f.write(os.urandom(3000) + b"\x00")
            status, body = self.extract(junk, ocr=False)
            self.assertEqual((status, body["error"]["code"]), (422, "UNSUPPORTED_FORMAT"))
            empty = os.path.join(tmp, "empty.docx")
            open(empty, "wb").close()
            status, body = self.extract(empty, ocr=False)
            self.assertEqual((status, body["error"]["code"]), (422, "UNREADABLE_FILE"))
        finally:
            shutil.rmtree(tmp, ignore_errors=True)

    def test_extra_image_formats_are_read_by_ocr(self):
        for name in ("scan.bmp", "scan.webp", "scan.gif", "scan.jfif"):
            status, body = self.extract(os.path.join(FX, name))
            self.assertEqual(status, 200, name)
            self.assertIn("采购申请单", body["data"]["text"], name)
            self.assertTrue(body["data"]["ocr_used"], name)
            self.assertEqual(body["data"]["content_status"], "ok", name)

    def test_multipage_tiff(self):
        status, body = self.extract(os.path.join(FX, "scan_2pages.tif"))
        self.assertEqual(status, 200)
        self.assertEqual(len(body["data"]["pages"]), 2)
        self.assertIn("采购申请单", body["data"]["pages"][0]["text"])
        self.assertIn("财务部", body["data"]["pages"][1]["text"])
        status, body = self.preview("scan_2pages.tif")
        self.assertEqual((status, body["data"]), (200, {"kind": "image", "pages": 2}))
        status, body = self.render("scan_2pages.tif", page=2, max_side=600)
        self.assertEqual((status, body["data"]["pages"], body["data"]["page"]), (200, 2, 2))
        status, body = self.render("scan_2pages.tif", page=3)
        self.assertEqual((status, body["error"]["code"]), (400, "INVALID_REQUEST"))
        status, body = self.render("scan.webp", max_side=500)
        self.assertEqual((status, body["data"]["pages"]), (200, 1))

    def test_blank_image_reports_ocr_empty(self):
        from PIL import Image
        tmp = tempfile.mkdtemp(dir=FX)
        try:
            p = os.path.join(tmp, "blank.png")
            Image.new("RGB", (400, 300), "white").save(p)
            status, body = self.extract(p)
            self.assertEqual(status, 200)
            self.assertEqual((body["data"]["content_status"], body["data"]["content_reason"]), ("unreadable", "ocr_empty"))
            self.assertIn("OCR_NO_TEXT", body["data"]["warnings"])
        finally:
            shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    unittest.main()
