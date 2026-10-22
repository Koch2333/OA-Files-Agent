package harness

import (
	"context"
	"strings"
	"testing"
)

const longPad = "本文件用于说明相关事项，请相关人员知悉并按要求办理，如有疑问请联系经办人。\n"

func TestHeuristicAccuracy(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		text     string
		mime     string
		dept     string // "" = nil
		typ      string
		date     string
		review   bool
		evidence []string // substrings that must appear in evidence
	}{
		{name: "filename only", filename: "财务部报销申请_20260315.docx", text: "金额合计一百元整，请财务核对并按流程办理报销事宜，谢谢。", dept: "财务部", typ: "申请", date: "2026-03-15", evidence: []string{"文件名含“财务部”", "文件名含“20260315”"}},
		{name: "body only labels", filename: "scan001.pdf", text: "发文部门：人事部\n关于2026年度招聘计划的通知\n" + longPad + "日期：2026-03-15", dept: "人事部", typ: "通知", date: "2026-03-15", evidence: []string{"正文含“发文部门：人事部”", "正文标题含“通知”"}},
		{name: "date slash standalone", filename: "x.docx", text: "采购部\n关于办公用品采购的通知\n" + longPad + "2026/3/15", dept: "采购部", typ: "通知", date: "2026-03-15"},
		{name: "date 8 digits filename", filename: "行政部_通知_20260315.pdf", text: longPad, dept: "行政部", typ: "通知", date: "2026-03-15"},
		{name: "chinese numeral date", filename: "scan.png", mime: "image/png", text: "制表部门：财务部\n费用报表\n" + longPad + "二〇二六年三月十五日", dept: "财务部", typ: "报表", date: "2026-03-15"},
		{name: "chinese date labeled", filename: "a.docx", text: "所属部门：行政部\n文件类型：制度\n" + longPad + "发文日期：2026年3月15日", dept: "行政部", typ: "制度", date: "2026-03-15"},
		{name: "synonym 薪酬", filename: "薪酬调整通知.docx", text: longPad + "自2026年4月1日起执行。", dept: "人事部", typ: "通知", date: "", review: true},
		{name: "synonym 供应商 contract", filename: "供应商合同_2026-03-15.pdf", text: longPad, dept: "采购部", typ: "合同", date: "2026-03-15"},
		{name: "synonym 办公室", filename: "办公室会议纪要20260315.docx", text: longPad, dept: "行政部", typ: "会议纪要", date: "2026-03-15"},
		{name: "filename convention beats body keywords", filename: "法务部_原材料采购框架合同_2025-10-01.pdf", text: longPad + "采购 采购 付款", dept: "法务部", typ: "合同", date: "2025-10-01"},
		{name: "open vocabulary department from filename", filename: "研发部_新一代智能检测系统研发项目进度推进会会议纪要_2026-03-12.docx", text: longPad, dept: "研发部", typ: "会议纪要", date: "2026-03-12"},
		{name: "literal 人力资源部 is not renamed", filename: "人力资源部_2026年3月全公司员工考勤汇总表_2026-03-31.xlsx", text: longPad, dept: "人力资源部", typ: "报表", date: "2026-03-31"},
		{name: "OCR-merged label and value is not a department", filename: "scan002.png", mime: "image/png", text: "劳动合同续签协议\n用人部门人事部\n员工姓名：张伟\n签订日期：2026年1月5日\n" + longPad, dept: "人事部", typ: "合同", date: "2026-01-05", review: true},
		{name: "label word in a standalone line is ignored", filename: "scan003.png", mime: "image/png", text: "申请部门\n" + longPad, dept: "", typ: "申请", date: "", review: true},
		{name: "letterhead names department", filename: "scan001.pdf", text: "销售部\n" + longPad, dept: "销售部", typ: "", date: "", review: true},
		{name: "conflict dept filename vs label", filename: "财务部通知.docx", text: "发文部门：人事部\n" + longPad, dept: "", typ: "通知", date: "", review: true, evidence: nil},
		{name: "conflict date filename vs body", filename: "财务部报销申请_20260301.docx", text: "日期：2026年3月15日\n" + longPad, dept: "财务部", typ: "申请", date: "", review: true},
		{name: "no signal", filename: "IMG_0001.jpg", mime: "image/jpeg", text: "hello world 12345 abcdefghijklmnop", dept: "", typ: "", date: "", review: true},
		{name: "empty text image", filename: "photo.png", mime: "image/png", text: "", dept: "", typ: "", date: "", review: true},
		{name: "single weak mention", filename: "notes.docx", text: longPad + "提到会计一次。", dept: "", typ: "", date: "", review: true},
		{name: "impossible date", filename: "申请表.docx", text: "日期：2026年2月30日\n" + longPad, dept: "", typ: "申请", date: "", review: true},
		{name: "multiple unlabeled dates", filename: "财务部报告.docx", text: longPad + "于2026年1月5日开会，2026年2月6日复核。", dept: "财务部", typ: "报告", date: "", review: true},
		{name: "expiry date excluded", filename: "采购部合同.docx", text: longPad + "有效日期：2027年1月1日", dept: "采购部", typ: "合同", date: "", review: true},
		{name: "future implausible year", filename: "财务部申请_20990101.docx", text: longPad, dept: "财务部", typ: "申请", date: "", review: true},
		{name: "dept and type conflicting types", filename: "采购合同.docx", text: "文件类型：通知\n" + longPad, dept: "采购部", typ: "", date: "", review: true},
		{name: "raw department outside vocab", filename: "doc.docx", text: "发文部门：技术部\n关于系统升级的通知\n" + longPad + "2026年3月15日", dept: "技术部", typ: "通知", date: "2026-03-15"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ext := &fakeExtractor{text: tc.text}
			a := NewAgent(Heuristic{}, ext, testOpts())
			mime := tc.mime
			if mime == "" {
				mime = "application/octet-stream"
			}
			res, err := a.AnalyzeFile(context.Background(), fileIn(tc.filename, mime), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Trim(deref(res.Department), "<nil>"); got != tc.dept {
				t.Errorf("department = %q, want %q (warnings %v)", deref(res.Department), tc.dept, res.Warnings)
			}
			if got := strings.Trim(deref(res.DocumentType), "<nil>"); got != tc.typ {
				t.Errorf("type = %q, want %q", deref(res.DocumentType), tc.typ)
			}
			if got := strings.Trim(deref(res.DocumentDate), "<nil>"); got != tc.date {
				t.Errorf("date = %q, want %q", deref(res.DocumentDate), tc.date)
			}
			if res.ReviewRequired != tc.review {
				t.Errorf("review = %v, want %v", res.ReviewRequired, tc.review)
			}
			all := strings.Join(res.Evidence, "\n")
			for _, e := range tc.evidence {
				if !strings.Contains(all, e) {
					t.Errorf("evidence missing %q in %q", e, all)
				}
			}
			if (res.Department != nil || res.DocumentType != nil || res.DocumentDate != nil) && len(res.Evidence) == 0 {
				t.Error("fields set without evidence")
			}
		})
	}
}

func TestHeuristicOCRDecision(t *testing.T) {
	long := "发文部门：财务部\n" + longPad
	tests := []struct {
		name, file, mime, text string
		wantOCR                bool
	}{
		{"image always OCR", "a.png", "image/png", long, true},
		{"empty pdf OCR", "a.pdf", "application/pdf", "", true},
		{"nearly empty OCR", "a.pdf", "application/pdf", "  1  ", true},
		{"native text no OCR", "a.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", long, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ext := &fakeExtractor{text: tc.text, ocrText: long}
			a := NewAgent(Heuristic{}, ext, testOpts())
			res, err := a.AnalyzeFile(context.Background(), fileIn(tc.file, tc.mime), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := ext.ocrCalls() > 0; got != tc.wantOCR {
				t.Fatalf("ocr called = %v, want %v", got, tc.wantOCR)
			}
			if tc.wantOCR && deref(res.Department) != "财务部" {
				t.Errorf("OCR text not used: %v", deref(res.Department))
			}
		})
	}
}

func TestHeuristicScriptOrder(t *testing.T) {
	ext := &fakeExtractor{text: "发文部门：财务部\n" + longPad}
	a := NewAgent(Heuristic{}, ext, testOpts())
	res, _ := a.AnalyzeFile(context.Background(), fileIn("a.docx", "application/octet-stream"), nil, nil)
	var names []string
	for _, c := range res.Trace {
		names = append(names, c.Name)
	}
	want := "inspect_file,extract_text,detect_template,extract_metadata"
	if strings.Join(names, ",") != want {
		t.Fatalf("trace = %v, want %s", names, want)
	}
}

func TestDateFormats(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2026年3月15日", "2026-03-15"},
		{"2026-03-15", "2026-03-15"},
		{"2026/3/15", "2026-03-15"},
		{"2026.03.15", "2026-03-15"},
		{"20260315", "2026-03-15"},
		{"二〇二六年三月十五日", "2026-03-15"},
		{"二〇二六年十二月三十一日", "2026-12-31"},
		{"二〇二五年十月二十日", "2025-10-20"},
		{"2026年13月1日", ""},
		{"2026-02-29", ""},
		{"20261340", ""},
		{"1899-01-01", ""},
		{"13800138000", ""},
	}
	for _, tc := range tests {
		hs := findDates(tc.in, fixedNow())
		got := ""
		if len(hs) > 0 {
			got = hs[0].iso()
		}
		if got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}
