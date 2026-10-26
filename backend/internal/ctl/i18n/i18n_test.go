package i18n

import "testing"

func TestT(t *testing.T) {
	t.Setenv("OA_LANG", "")
	if T("中", "en") != "中" {
		t.Fatal("default must stay Chinese")
	}
	t.Setenv("OA_LANG", "en")
	if T("中", "en") != "en" {
		t.Fatal("OA_LANG=en")
	}
	t.Setenv("OA_LANG", "EN_us")
	if !English() {
		t.Fatal("case-insensitive")
	}
	t.Setenv("OA_LANG", "zh")
	if T("中", "en") != "中" {
		t.Fatal("zh")
	}
}
