package formats

import (
	"os"
	"testing"
)

func TestTable(t *testing.T) {
	for name, want := range map[string]string{
		"a.DOCX": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"a.doc":  "application/msword", "b.XLS": "application/vnd.ms-excel", "c.ppt": "application/vnd.ms-powerpoint",
		"d.jfif": "image/jpeg", "e.tiff": "image/tiff", "f.zip": OctetStream, "noext": OctetStream, "x.csv": "text/csv",
	} {
		if got := MIME(name); got != want {
			t.Errorf("%s: %s want %s", name, got, want)
		}
	}
	if !Extractable("a.msg") || Extractable("a.exe") || Extractable("a.zip") {
		t.Fatal("extractable")
	}
	if f, _ := Lookup(".doc"); !f.Legacy || f.Preview != "document" {
		t.Fatal(f)
	}
	if f, _ := Lookup("report.xls"); f.Preview != "spreadsheet" {
		t.Fatal(f)
	}
}

func TestJunk(t *testing.T) {
	for _, n := range []string{"Thumbs.db", "dir/.DS_Store", "~$report.docx", "desktop.ini", "._x.pdf"} {
		if !IsJunk(n) {
			t.Errorf("%s should be junk", n)
		}
	}
	if IsJunk("report.docx") || IsJunk("a/b/.gitignore") {
		t.Fatal("real file flagged")
	}
}

// The embedded copy must be byte-identical to the contract source (scripts/sync_formats.py keeps it so).
func TestEmbeddedCopyMatchesContract(t *testing.T) {
	src, err := os.ReadFile("../../../contracts/formats.json")
	if err != nil {
		t.Skip("contracts/formats.json not available")
	}
	if string(src) != string(raw) {
		t.Fatal("backend/internal/formats/formats.json is stale: run python scripts/sync_formats.py")
	}
}
