package archive

import (
	"strings"
	"testing"
)

func sp(s string) *string { return &s }

func TestRenderChinese(t *testing.T) {
	rule := Rule{NamingPattern: "{date}_{department}_{type}_{stem}{ext}", DirectoryPattern: "{department}/{type}/{year}"}
	name, dir, warns, err := Render(rule, "采购合同.pdf", Meta{Department: sp("财务部"), DocumentType: sp("合同"), Date: sp("2024-05-06")})
	if err != nil {
		t.Fatal(err)
	}
	if name != "2024-05-06_财务部_合同_采购合同.pdf" || dir != "财务部/合同/2024" || len(warns) != 0 {
		t.Fatalf("got %q %q %v", name, dir, warns)
	}
}

func TestRenderPlaceholdersAll(t *testing.T) {
	name, dir, _, err := Render(Rule{"{year}{month}{day}-{original}", "{year}/{month}/{day}"}, "a.docx", Meta{Date: sp("2023-01-09")})
	if err != nil || name != "20230109-a.docx" || dir != "2023/01/09" {
		t.Fatal(name, dir, err)
	}
}

func TestRenderExtensionPreservedAndDefault(t *testing.T) {
	name, _, _, err := Render(Rule{NamingPattern: "报告"}, "x.PDF", Meta{})
	if err != nil || name != "报告.PDF" {
		t.Fatal(name, err)
	}
	name, _, _, err = Render(Rule{}, "orig.xlsx", Meta{})
	if err != nil || name != "orig.xlsx" {
		t.Fatal(name, err)
	}
}

func TestRenderUnknownPlaceholder(t *testing.T) {
	for _, r := range []Rule{{NamingPattern: "{foo}"}, {DirectoryPattern: "{bar}"}, {NamingPattern: "{date"}, {NamingPattern: "a}"}, {NamingPattern: "a/b"}} {
		if _, _, _, err := Render(r, "a.pdf", Meta{}); err == nil {
			t.Errorf("rule %+v should fail", r)
		}
	}
}

func TestRenderMissingMetaFallbacksWarn(t *testing.T) {
	rule := Rule{NamingPattern: "{date}_{department}_{type}{ext}", DirectoryPattern: "{department}/{type}/{year}"}
	name, dir, warns, err := Render(rule, "a.pdf", Meta{Department: sp("  ")})
	if err != nil {
		t.Fatal(err)
	}
	if name != "未知日期_待审核_待审核.pdf" || dir != "待审核/待审核/未知日期" {
		t.Fatalf("%q %q", name, dir)
	}
	if len(warns) != 3 {
		t.Fatalf("want 3 deduped warnings, got %v", warns)
	}
	_, _, warns, _ = Render(Rule{NamingPattern: "{date}{ext}"}, "a.pdf", Meta{Date: sp("2024/01/01")})
	if len(warns) != 1 || !strings.Contains(warns[0], "日期") {
		t.Fatal(warns)
	}
}

func TestRenderWindowsIllegalAndTraversal(t *testing.T) {
	rule := Rule{NamingPattern: "{department}-{stem}{ext}", DirectoryPattern: "{department}/{type}"}
	m := Meta{Department: sp(`..\..\etc/pa:ss*w?d"<>|`), DocumentType: sp("..")}
	name, dir, _, err := Render(rule, `C:\evil\a:b*c.pdf`, m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(name+dir, `\:*?"<>|`) || strings.Contains(dir, "..") {
		t.Fatalf("unsafe: %q %q", name, dir)
	}
	if err := ValidateRelative(dir + "/" + name); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(name, ".pdf") {
		t.Fatal(name)
	}
}

func TestRenderPatternTraversalInDirectory(t *testing.T) {
	_, dir, _, err := Render(Rule{DirectoryPattern: `../../x/..\y/./z`}, "a.pdf", Meta{})
	if err != nil {
		t.Fatal(err)
	}
	if dir != "x/y/z" {
		t.Fatalf("dir %q", dir)
	}
}

func TestRenderReservedAndTrailing(t *testing.T) {
	rule := Rule{NamingPattern: "{department}{ext}", DirectoryPattern: "{department}/{type}"}
	name, dir, _, err := Render(rule, "a.pdf", Meta{Department: sp("CON"), DocumentType: sp("aux. ")})
	if err != nil {
		t.Fatal(err)
	}
	if dir != "_CON/_aux" || name != "_CON.pdf" {
		t.Fatalf("%q %q", dir, name)
	}
	name, _, _, _ = Render(Rule{NamingPattern: "COM1"}, "a.txt", Meta{})
	if name != "_COM1.txt" {
		t.Fatal(name)
	}
	name, _, _, _ = Render(Rule{NamingPattern: "report. . "}, "a.txt", Meta{})
	if name != "report.txt" {
		t.Fatal(name)
	}
}

func TestRenderLongNames(t *testing.T) {
	long := strings.Repeat("长", 300)
	name, dir, warns, err := Render(Rule{NamingPattern: "{stem}{ext}", DirectoryPattern: "{department}"}, long+".pdf", Meta{Department: sp(long)})
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range strings.Split(dir, "/") {
		if runeLen(seg) > 120 {
			t.Fatal("segment too long")
		}
	}
	if runeLen(name) > 120 || !strings.HasSuffix(name, ".pdf") {
		t.Fatalf("name %d %q", runeLen(name), name[len(name)-8:])
	}
	if runeLen(dir+"/"+name) > 240 {
		t.Fatal("total too long")
	}
	if len(warns) == 0 {
		t.Fatal("truncation should warn")
	}
	if _, _, _, err := Render(Rule{DirectoryPattern: "{department}/{department}/{department}"}, "a.pdf", Meta{Department: sp(long)}); err == nil {
		t.Fatal("overlong directory must error")
	}
}

func TestRenderNFC(t *testing.T) {
	name, _, _, err := Render(Rule{NamingPattern: "{stem}{ext}"}, "Cafe\u0301.pdf", Meta{})
	if err != nil || name != "Caf\u00e9.pdf" {
		t.Fatalf("%q %v", name, err)
	}
}

func TestValidateRelative(t *testing.T) {
	good := []string{"a.pdf", "财务部/合同/2024/a.pdf", "a b/c.txt"}
	for _, g := range good {
		if err := ValidateRelative(g); err != nil {
			t.Errorf("%q: %v", g, err)
		}
	}
	bad := []string{"", "/abs", "../x", "a/../b", "a/..", `a\b`, `..\x`, "C:/x", "C:x", `\\srv\share\x`, "//srv/share/x", "a//b", "a/", "./a",
		"a.", "a /b", "CON", "dir/nul.txt", "a\x00b", "a\tb", "a|b", strings.Repeat("x", 121), strings.Repeat("ab/", 100) + "c"}
	for _, b := range bad {
		if err := ValidateRelative(b); err == nil {
			t.Errorf("%q should be rejected", b)
		}
	}
}

func TestPlanConflicts(t *testing.T) {
	disk := map[string]bool{"d/a.pdf": true, "d/b_2.pdf": true}
	items := []PlanItem{
		{"1", "d", "a.pdf"}, {"2", "d", "a.pdf"}, {"3", "d", "A.PDF"}, {"4", "d", "b.pdf"}, {"5", "e", "a.pdf"}, {"6", "", "root"}, {"7", "d", "a_2.PDF"},
	}
	got := Plan(func(rel string) bool { return disk[rel] }, items)
	want := []string{"d/a_2.pdf", "d/a_3.pdf", "d/A.PDF", "d/b.pdf", "e/a.pdf", "root", "d/a_2_2.PDF"}
	for i, w := range want {
		if got[i].Rel != w {
			t.Errorf("item %d: got %q want %q", i, got[i].Rel, w)
		}
	}
	if !got[0].Renamed || len(got[0].Warnings) != 1 || got[0].Warnings[0] != "目标文件名已存在，已自动追加序号" {
		t.Fatal(got[0])
	}
	if got[4].Renamed || len(got[4].Warnings) != 0 {
		t.Fatal(got[4])
	}
	if Plan(nil, nil) == nil {
		t.Fatal("want non-nil empty")
	}
}
