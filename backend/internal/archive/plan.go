package archive

import (
	"path"
	"strings"
)

// WarnConflict is attached to items whose name was changed to avoid a conflict.
const WarnConflict = "目标文件名已存在，已自动追加序号"

// PlanItem is one desired destination (Dir/Name as produced by Render).
type PlanItem struct{ FileID, Dir, Name string }

// Planned is the conflict-free outcome for one item.
type Planned struct {
	FileID   string
	Dir      string
	Name     string
	Rel      string
	Renamed  bool
	Warnings []string
}

// Plan assigns unique destinations within the plan and against existing(rel)
// (may be nil) by appending _2, _3 ... before the extension. Uniqueness is
// case-insensitive (Windows). It never overwrites anything.
func Plan(existing func(rel string) bool, items []PlanItem) []Planned {
	taken := map[string]bool{}
	key := func(rel string) string { return strings.ToLower(nfc(rel)) }
	out := make([]Planned, 0, len(items))
	for _, it := range items {
		name := it.Name
		rel := joinRel(it.Dir, name)
		n := 1
		for taken[key(rel)] || (existing != nil && existing(rel)) {
			n++
			name = suffixName(it.Name, n, it.Dir)
			rel = joinRel(it.Dir, name)
		}
		taken[key(rel)] = true
		p := Planned{FileID: it.FileID, Dir: it.Dir, Name: name, Rel: rel, Renamed: n > 1}
		if p.Renamed {
			p.Warnings = []string{WarnConflict}
		}
		out = append(out, p)
	}
	return out
}

// suffixName returns name with _n inserted before the extension, shortening the
// stem if the total path would exceed the cap.
func suffixName(name string, n int, dir string) string {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		stem, ext = name, ""
	}
	suf := "_" + itoa(n)
	r := []rune(stem)
	for len(r) > 1 && runeLen(joinRel(dir, string(r)+suf+ext)) > maxTotalRunes {
		r = r[:len(r)-1]
	}
	return string(r) + suf + ext
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
