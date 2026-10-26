package archive

import (
	"strings"
	"testing"
)

func TestValidateCategory(t *testing.T) {
	for _, ok := range []string{"", "  ", "财务部", "R&D 部", "其他", strings.Repeat("好", 60)} {
		if err := ValidateCategory(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"a/b", `a\b`, "a:b", "a*", "a\x00b", "..", " . ", strings.Repeat("好", 61), "a‮b"} {
		if err := ValidateCategory(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestDisplayCategory(t *testing.T) {
	for in, want := range map[string]string{"": "其他", "  ": "其他", "待审核": "其他", "其他": "其他", "财务部": "财务部"} {
		if got := DisplayCategory(in); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
}

func TestMergeCategoryOptions(t *testing.T) {
	got := MergeCategoryOptions(
		[]string{"财务部", "人事部"},
		[]UsedValue{{"法务部", 2}, {"人事部 ", 7}, {"其他", 9}, {"", 3}, {"待审核", 1}, {"bad/name", 4}},
		[]UsedValue{{"法务部", 1}, {"研发部", 3}},
		0)
	var vals []string
	for _, o := range got {
		vals = append(vals, o.Value)
	}
	if strings.Join(vals, ",") != "财务部,人事部,法务部,研发部,其他" {
		t.Fatalf("order/dedupe: %v", vals)
	}
	if got[1].Count != 7 || got[2].Count != 3 || got[0].Count != 0 || got[0].Source != CategorySourceBuiltin || got[2].Source != CategorySourceLedger || got[3].Source != CategorySourcePlan {
		t.Fatalf("counts/sources: %+v", got)
	}
	if last := got[len(got)-1]; last.Value != CategoryOther || last.Source != CategorySourceReserved {
		t.Fatalf("其他 must be last: %+v", last)
	}
}

func TestMergeCategoryOptionsCaseAndNormalisation(t *testing.T) {
	got := MergeCategoryOptions(nil, []UsedValue{{"ABC部", 1}, {"abc部", 5}, {"é", 1}, {"é", 1}}, nil, 0)
	if len(got) != 3 || got[0].Value != "abc部" || got[0].Count != 6 { // most-used spelling is seen first
		t.Fatalf("%+v", got)
	}
}

func TestMergeCategoryOptionsCap(t *testing.T) {
	var used []UsedValue
	for i := 0; i < 500; i++ {
		used = append(used, UsedValue{Value: "部" + string(rune(0x4e00+i)), Count: 500 - i})
	}
	got := MergeCategoryOptions(nil, used, nil, 0)
	if len(got) != MaxCategoryOptions || got[len(got)-1].Value != CategoryOther || got[0].Count != 500 {
		t.Fatalf("cap: %d %+v", len(got), got[len(got)-1])
	}
	if small := MergeCategoryOptions(nil, used, nil, 5); len(small) != 5 || small[4].Value != CategoryOther {
		t.Fatalf("limit 5: %+v", small)
	}
}

func TestRenderUsesOtherLiterallyInPathAndName(t *testing.T) {
	o := CategoryOther
	d := "2026-03-15"
	rule := Rule{NamingPattern: "{date}_{department}_{type}_{original}", DirectoryPattern: "{department}/{year}/{type}"}
	name, dir, warns, err := Render(rule, "a.pdf", Meta{Department: &o, DocumentType: &o, Date: &d})
	if err != nil || dir != "其他/2026/其他" || name != "2026-03-15_其他_其他_a.pdf" || len(warns) != 0 {
		t.Fatalf("%q %q %v %v", name, dir, warns, err)
	}
}
