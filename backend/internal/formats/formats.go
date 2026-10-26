// Package formats is the Go view of contracts/formats.json (copied here by scripts/sync_formats.py and embedded):
// the single table of file extensions the product accepts, with MIME type, preview kind and extractor family.
//
// "Accepted" and "content-extractable" are different things: every file is accepted for import (an unreadable or
// unknown one is archived by its name only, see the pipeline), while only the extensions in this table have a
// content extractor in the worker.
package formats

import (
	_ "embed"
	"encoding/json"
	"path"
	"sort"
	"strings"
)

//go:embed formats.json
var raw []byte

// Format is one row of the table.
type Format struct {
	Ext        string `json:"ext"`
	MIME       string `json:"mime"`
	Group      string `json:"group"`
	Label      string `json:"label"`
	Extractor  string `json:"extractor"`
	Preview    string `json:"preview"`
	Icon       string `json:"icon"`
	Legacy     bool   `json:"legacy"`
	BestEffort bool   `json:"best_effort"`
}

type table struct {
	Version int               `json:"version"`
	Formats []Format          `json:"formats"`
	Hints   map[string]string `json:"unsupported_hints"`
}

// OctetStream is the MIME type of everything without a table entry.
const OctetStream = "application/octet-stream"

var (
	byExt = map[string]Format{}
	exts  []string
	hints map[string]string
)

func init() {
	var t table
	if err := json.Unmarshal(raw, &t); err != nil || len(t.Formats) == 0 {
		panic("formats: embedded formats.json is invalid")
	}
	for _, f := range t.Formats {
		if !strings.HasPrefix(f.Ext, ".") || f.Ext != strings.ToLower(f.Ext) || f.MIME == "" {
			panic("formats: bad row " + f.Ext)
		}
		byExt[f.Ext] = f
		exts = append(exts, f.Ext)
	}
	hints = t.Hints
}

// Ext returns the lower-cased extension of a file name (with the dot).
func Ext(name string) string { return strings.ToLower(path.Ext(strings.ReplaceAll(name, "\\", "/"))) }

// Lookup finds the table row for an extension (".docx") or a file name.
func Lookup(extOrName string) (Format, bool) {
	e := strings.ToLower(extOrName)
	if !strings.HasPrefix(e, ".") || strings.ContainsAny(e, "/\\") {
		e = Ext(extOrName)
	}
	f, ok := byExt[e]
	return f, ok
}

// Extractable reports whether the worker can read the content of a file with this name.
func Extractable(name string) bool { _, ok := Lookup(name); return ok }

// MIME returns the MIME type for a file name (OctetStream when the extension is not in the table).
func MIME(name string) string {
	if f, ok := Lookup(name); ok {
		return f.MIME
	}
	return OctetStream
}

// Extensions lists the extractable extensions, sorted.
func Extensions() []string {
	out := append([]string(nil), exts...)
	sort.Strings(out)
	return out
}

// Hint returns the user-facing explanation for a name that cannot be read by content ("" when none).
func Hint(key string) string { return hints[key] }

// IsJunk reports operating-system and Office housekeeping files that a folder import must not pick up.
func IsJunk(name string) bool {
	base := strings.ToLower(path.Base(strings.ReplaceAll(name, "\\", "/")))
	switch base {
	case "thumbs.db", "ehthumbs.db", "desktop.ini", ".ds_store", ".localized", "icon\r":
		return true
	}
	return strings.HasPrefix(base, "~$") || strings.HasPrefix(base, "._") || strings.HasPrefix(base, ".~lock.")
}
