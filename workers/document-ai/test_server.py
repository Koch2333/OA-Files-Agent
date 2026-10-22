import json
import os
import shutil
import sys
import tempfile
import threading
import unittest
import urllib.error
import urllib.request

from server import create_server

TOKEN = "a" * 32
SAMPLES = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "tests", "samples"))


class ServerCase(unittest.TestCase):
    spool = SAMPLES
    timeout = 120
    child_cmd = None

    @classmethod
    def setUpClass(cls):
        cls.server = create_server(0, TOKEN, spool_root=cls.spool, timeout=cls.timeout, child_cmd=cls.child_cmd)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.base = f"http://127.0.0.1:{cls.server.server_port}"

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()

    def call(self, method, route, body=None, auth=True):
        data = json.dumps(body).encode("utf-8") if body is not None else None
        headers = {"Content-Type": "application/json"}
        if auth:
            headers["Authorization"] = "Bearer " + TOKEN
        request = urllib.request.Request(self.base + route, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(request) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as err:
            return err.code, json.load(err)

    def extract(self, path, filename=None, ocr=True):
        return self.call("POST", "/v1/extract", {"path": path, "filename": filename or os.path.basename(path),
                                                 "mime_type": "", "ocr": ocr})

    def sample(self, name):
        return os.path.join(SAMPLES, name)


class AuthAndHealthTest(ServerCase):
    def test_auth_and_health(self):
        self.assertEqual(self.call("GET", "/healthz", auth=False)[0], 401)
        status, body = self.call("GET", "/healthz")
        self.assertEqual(status, 200)
        self.assertEqual(body["data"]["status"], "ok")
        self.assertIs(body["data"]["extraction_available"], True)
        self.assertIs(body["data"]["ocr_available"], True)

    def test_extract_requires_auth(self):
        status, body = self.call("POST", "/v1/extract", {"path": "x"}, auth=False)
        self.assertEqual((status, body["error"]["code"]), (401, "UNAUTHORIZED"))

    def test_missing_token_rejected(self):
        with self.assertRaises(ValueError):
            create_server(0, "short")

    def test_loopback_only(self):
        self.assertEqual(self.server.server_address[0], "127.0.0.1")

    def test_invalid_requests(self):
        for body in ({}, {"path": "relative/a.docx"}, {"path": 5}, {"path": self.sample("x.docx"), "ocr": "yes"}):
            status, resp = self.call("POST", "/v1/extract", body)
            self.assertEqual((status, resp["error"]["code"]), (400, "INVALID_REQUEST"), body)


class ExtractFormatsTest(ServerCase):
    def ok(self, name, *needles, ocr_used=False):
        status, body = self.extract(self.sample(name))
        self.assertEqual(status, 200, body)
        data = body["data"]
        for needle in needles:
            self.assertIn(needle, data["text"])
        self.assertIs(data["ocr_used"], ocr_used)
        self.assertIs(data["truncated"], False)
        self.assertEqual(set(data["metadata"]), {"title", "author", "created", "modified"})
        for page in data["pages"]:
            self.assertIn(page["source"], ("native", "ocr"))
        return data

    def test_docx_with_table(self):
        self.ok("财务部_2026年3月报销申请.docx", "申请部门：财务部", "差旅费", "3280.00")

    def test_docx_header(self):
        self.ok("关于2026年国庆节放假安排的通知.docx", "行政部文件", "发文日期：2026-09-28", "放假调休")

    def test_xlsx(self):
        data = self.ok("采购部_2026年Q1设备采购报表.xlsx", "采购明细", "汇总", "联想科技", "2026-03-31")
        self.assertEqual(len(data["pages"]), 2)
        self.ok("2026年2月考勤汇总报表.xlsx", "编制部门：人事部", "王芳")

    def test_native_pdf(self):
        data = self.ok("办公电脑采购合同.pdf", "办公电脑采购合同", "2026年3月15日")
        self.assertEqual(data["pages"][0]["source"], "native")
        self.ok("行政部_会议室使用申请.pdf", "会议室使用申请")

    def test_scanned_pdf_uses_ocr(self):
        data = self.ok("劳动合同续签_扫描件.pdf", "劳动合同续签协议", "人事部", ocr_used=True)
        self.assertEqual(data["pages"][0]["source"], "ocr")
        self.assertGreater(data["pages"][0]["confidence"], 0.5)
        self.ok("scan_20260520_001.pdf", "发文部门：财务部", "2026-05-20", ocr_used=True)

    def test_images_ocr(self):
        self.ok("采购申请单_拍照.png", "采购申请单", "采购部", ocr_used=True)
        self.ok("人事部_入职培训通知.jpg", "入职培训通知", "2026-07-01", ocr_used=True)

    def test_ocr_disabled_for_scanned_pdf(self):
        status, body = self.extract(self.sample("劳动合同续签_扫描件.pdf"), ocr=False)
        self.assertEqual(status, 200)
        self.assertFalse(body["data"]["ocr_used"])
        self.assertEqual(body["data"]["text"], "")
        self.assertTrue(body["data"]["warnings"])

    def test_corrupt_and_encrypted(self):
        status, body = self.extract(self.sample("财务部_损坏的报表.docx"))
        self.assertEqual((status, body["error"]["code"]), (422, "UNREADABLE_FILE"))
        status, body = self.extract(self.sample("加密合同.pdf"))
        self.assertEqual((status, body["error"]["code"]), (422, "ENCRYPTED"))

    def test_unsupported_and_missing(self):
        status, body = self.extract(self.sample("manifest.json"))
        self.assertEqual((status, body["error"]["code"]), (422, "UNSUPPORTED_FORMAT"))
        status, body = self.extract(self.sample("不存在.docx"))
        self.assertEqual((status, body["error"]["code"]), (404, "FILE_NOT_FOUND"))


class SpoolTest(ServerCase):
    spool = None

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.mkdtemp()
        cls.spool = os.path.join(cls.tmp, "spool")
        cls.outside = os.path.join(cls.tmp, "outside")
        os.makedirs(cls.spool)
        os.makedirs(cls.outside)
        super().setUpClass()

    @classmethod
    def tearDownClass(cls):
        super().tearDownClass()
        shutil.rmtree(cls.tmp, ignore_errors=True)

    def test_path_outside_spool(self):
        shutil.copy(self.sample("办公电脑采购合同.pdf"), os.path.join(self.outside, "a.pdf"))
        status, body = self.extract(os.path.join(self.outside, "a.pdf"))
        self.assertEqual((status, body["error"]["code"]), (403, "PATH_NOT_ALLOWED"))

    def test_dotdot_escape(self):
        shutil.copy(self.sample("办公电脑采购合同.pdf"), os.path.join(self.outside, "b.pdf"))
        status, body = self.extract(os.path.join(self.spool, "..", "outside", "b.pdf"))
        self.assertEqual((status, body["error"]["code"]), (403, "PATH_NOT_ALLOWED"))

    def test_sibling_prefix_not_allowed(self):
        sibling = self.spool + "2"
        os.makedirs(sibling)
        shutil.copy(self.sample("办公电脑采购合同.pdf"), os.path.join(sibling, "c.pdf"))
        status, body = self.extract(os.path.join(sibling, "c.pdf"))
        self.assertEqual((status, body["error"]["code"]), (403, "PATH_NOT_ALLOWED"))

    def test_symlink_escape(self):
        target = os.path.join(self.outside, "secret.pdf")
        shutil.copy(self.sample("办公电脑采购合同.pdf"), target)
        link = os.path.join(self.spool, "link.pdf")
        try:
            os.symlink(target, link)
        except (OSError, NotImplementedError):
            self.skipTest("symlinks not permitted on this system")
        status, body = self.extract(link)
        self.assertEqual((status, body["error"]["code"]), (403, "PATH_NOT_ALLOWED"))

    def test_inside_spool_ok(self):
        dest = os.path.join(self.spool, "ok.pdf")
        shutil.copy(self.sample("办公电脑采购合同.pdf"), dest)
        status, _ = self.extract(dest)
        self.assertEqual(status, 200)

    def test_file_too_large(self):
        big = os.path.join(self.spool, "big.pdf")
        with open(big, "wb") as f:
            f.truncate(20 * 1024 * 1024 + 1)
        status, body = self.extract(big)
        self.assertEqual((status, body["error"]["code"]), (413, "FILE_TOO_LARGE"))

    def test_truncation(self):
        import docx
        d = docx.Document()
        for _ in range(60):
            d.add_paragraph("长文本" * 1200)
        dest = os.path.join(self.spool, "long.docx")
        d.save(dest)
        status, body = self.extract(dest)
        self.assertEqual(status, 200)
        self.assertTrue(body["data"]["truncated"])
        self.assertEqual(len(body["data"]["text"]), 200000)


class ChildIsolationBase(ServerCase):
    script = ""
    timeout = 2

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.mkdtemp()
        stub = os.path.join(cls.tmp, "stub.py")
        with open(stub, "w") as f:
            f.write(cls.script)
        cls.child_cmd = [sys.executable, "-I", stub]
        super().setUpClass()

    @classmethod
    def tearDownClass(cls):
        super().tearDownClass()
        shutil.rmtree(cls.tmp, ignore_errors=True)


class CrashIsolationTest(ChildIsolationBase):
    script = "import os\nos._exit(139)\n"

    def test_crash_returns_422_and_server_survives(self):
        status, body = self.extract(self.sample("办公电脑采购合同.pdf"))
        self.assertEqual((status, body["error"]["code"]), (422, "UNREADABLE_FILE"))
        self.assertEqual(self.call("GET", "/healthz")[0], 200)


class TimeoutIsolationTest(ChildIsolationBase):
    script = "import time\ntime.sleep(60)\n"

    def test_hang_returns_504_and_server_survives(self):
        status, body = self.extract(self.sample("办公电脑采购合同.pdf"))
        self.assertEqual((status, body["error"]["code"]), (504, "EXTRACT_TIMEOUT"))
        self.assertEqual(self.call("GET", "/healthz")[0], 200)


class GarbageOutputTest(ChildIsolationBase):
    script = "print('not json')\n"

    def test_garbage_output(self):
        status, body = self.extract(self.sample("办公电脑采购合同.pdf"))
        self.assertEqual((status, body["error"]["code"]), (422, "UNREADABLE_FILE"))


class TransientCrashIsRetriedTest(ChildIsolationBase):
    # fails on the first call (marker file absent), succeeds afterwards
    script = (
        "import json, os, sys\n"
        "marker = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'seen')\n"
        "if not os.path.exists(marker):\n"
        "    open(marker, 'w').close()\n"
        "    os._exit(1)\n"
        "print(json.dumps({'data': {'text': 'ok', 'pages': [], 'ocr_used': False, 'truncated': False, 'warnings': [], 'metadata': {}}}))\n"
    )

    def test_one_transient_crash_does_not_make_the_file_unreadable(self):
        status, body = self.extract(self.sample("办公电脑采购合同.pdf"))
        self.assertEqual(status, 200)
        self.assertEqual(body["data"]["text"], "ok")


if __name__ == "__main__":
    unittest.main()
