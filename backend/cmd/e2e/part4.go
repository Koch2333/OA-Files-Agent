package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var revealFlag = flagBool("reveal", "actually launch Windows Explorer once (opens a window)")

type fsEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	IsDir  bool   `json:"is_dir"`
	Ledger *struct {
		ID           string `json:"id"`
		OriginalName string `json:"original_name"`
	} `json:"ledger"`
}
type fsListing struct {
	Path         string    `json:"path"`
	Parent       *string   `json:"parent"`
	Entries      []fsEntry `json:"entries"`
	Capabilities struct {
		Reveal bool    `json:"reveal"`
		Root   *string `json:"root"`
	} `json:"capabilities"`
}

func listFS(p string) (int, fsListing) {
	st, d, _ := call("GET", "/v1/fs?path="+url.QueryEscape(p), nil, nil)
	var l fsListing
	_ = json.Unmarshal(d, &l)
	return st, l
}

func firstFile(p string, depth int) (fsEntry, bool) {
	_, l := listFS(p)
	for _, e := range l.Entries {
		if !e.IsDir && e.Ledger != nil {
			return e, true
		}
	}
	if depth > 0 {
		for _, e := range l.Entries {
			if e.IsDir {
				if f, ok := firstFile(e.Path, depth-1); ok {
					return f, true
				}
			}
		}
	}
	return fsEntry{}, false
}

func ledgerHas(q string) bool {
	_, d, _ := call("GET", "/v1/ledger?q="+url.QueryEscape(q), nil, nil)
	var l struct{ Total int }
	_ = json.Unmarshal(d, &l)
	return l.Total > 0
}

// part4 exercises the file manager: browse, preview/download, folders, rename/move with ledger sync,
// trash/restore, safety checks and (optionally) Explorer.
func part4() {
	println("--- part 4: file manager ---")
	st, root := listFS("")
	check("fs: root listing (200) with ledger-managed folders", st == 200 && len(root.Entries) > 0 && root.Parent == nil, st, root)
	check("fs: local caller sees archive root path", root.Capabilities.Root != nil && *root.Capabilities.Root != "", root.Capabilities)
	file, ok := firstFile("", 4)
	if !ok {
		check("fs: found a ledger-managed file to work with", false)
		return
	}
	check("fs: files carry ledger info", file.Ledger != nil && file.Ledger.OriginalName != "", file)

	res, err := http.Get(*base + "/v1/fs/content?path=" + url.QueryEscape(file.Path))
	if err == nil {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		check("fs: content served with nosniff and a filename", res.StatusCode == 200 && len(b) > 0 && res.Header.Get("X-Content-Type-Options") == "nosniff" && strings.Contains(res.Header.Get("Content-Disposition"), "filename*=UTF-8''"), res.StatusCode, res.Header)
	}
	req, _ := http.NewRequest("GET", *base+"/v1/fs/content?path="+url.QueryEscape(file.Path)+"&download=1", nil)
	req.Header.Set("Range", "bytes=0-3")
	if r2, err := http.DefaultClient.Do(req); err == nil {
		r2.Body.Close()
		check("fs: Range request + attachment on download=1", r2.StatusCode == 206 && strings.HasPrefix(r2.Header.Get("Content-Disposition"), "attachment"), r2.StatusCode, r2.Header.Get("Content-Disposition"))
	}
	for _, bad := range []string{"../../etc/passwd", ".journal/x", ".trash", "a/../../b"} {
		st, _, _ = call("GET", "/v1/fs/content?path="+url.QueryEscape(bad), nil, nil)
		check("fs: content refuses "+bad, st == 400 || st == 404, st)
		st, _, _ = call("GET", "/v1/fs?path="+url.QueryEscape(bad), nil, nil)
		check("fs: listing refuses "+bad, st == 400 || st == 404, st)
	}
	_, top := listFS("")
	for _, e := range top.Entries {
		check("fs: hidden internals are never listed ("+e.Name+")", !strings.HasPrefix(e.Name, "."))
	}

	// folders
	st, _, _ = call("POST", "/v1/fs/folders", map[string]any{"path": "e2e-夹"}, nil)
	check("fs: create folder (201)", st == 201, st)
	st, _, _ = call("POST", "/v1/fs/folders", map[string]any{"path": "e2e-夹"}, nil)
	check("fs: duplicate folder → 409", st == 409, st)
	st, _, _ = call("POST", "/v1/fs/folders", map[string]any{"path": ".hidden"}, nil)
	check("fs: dot-folder → 400", st == 400, st)
	st, _, _ = call("POST", "/v1/fs/folders", map[string]any{"path": "../escape"}, nil)
	check("fs: traversal folder → 400", st == 400, st)
	st, _, _ = call("POST", "/v1/fs/folders", map[string]any{"path": "不存在/子夹"}, nil)
	check("fs: missing parent → 404", st == 404, st)

	// move + ledger sync
	dest := "e2e-夹/" + file.Name
	st, d, _ := call("POST", "/v1/fs/move", map[string]any{"from": file.Path, "to": dest}, nil)
	var mv struct {
		LedgerUpdated int `json:"ledger_updated"`
	}
	_ = json.Unmarshal(d, &mv)
	check("fs: move file (200) updates exactly one ledger row", st == 200 && mv.LedgerUpdated == 1, st, string(d))
	check("fs: ledger now points at the new path", ledgerHas("e2e-夹"))
	st, _, _ = call("GET", "/v1/fs/content?path="+url.QueryEscape(file.Path), nil, nil)
	check("fs: old path is gone (404)", st == 404, st)
	st, _, _ = call("POST", "/v1/fs/move", map[string]any{"from": file.Path, "to": dest}, nil)
	check("fs: moving a missing source → 404", st == 404, st)
	st, _, _ = call("POST", "/v1/fs/folders", map[string]any{"path": "e2e-夹/占位"}, nil)
	st, _, _ = call("POST", "/v1/fs/move", map[string]any{"from": "e2e-夹/占位", "to": dest}, nil)
	check("fs: move onto an existing file → 409 (never overwrites)", st == 409, st)
	st, _, _ = call("POST", "/v1/fs/move", map[string]any{"from": "e2e-夹", "to": "e2e-夹/内部"}, nil)
	check("fs: folder into itself → 400", st == 400, st)
	st, _, _ = call("POST", "/v1/fs/move", map[string]any{"from": dest, "to": "../outside.pdf"}, nil)
	check("fs: move outside the root → 400", st == 400, st)

	// folder rename keeps ledger in sync
	st, d, _ = call("POST", "/v1/fs/move", map[string]any{"from": "e2e-夹", "to": "e2e-夹2"}, nil)
	_ = json.Unmarshal(d, &mv)
	check("fs: folder rename rewrites ledger paths below it", st == 200 && mv.LedgerUpdated == 1 && ledgerHas("e2e-夹2/"), st, string(d))

	// trash / restore
	st, d, _ = call("POST", "/v1/fs/trash", map[string]any{"path": "e2e-夹2"}, nil)
	var ti struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(d, &ti)
	check("fs: delete to trash (201)", st == 201 && ti.ID != "", st, string(d))
	check("fs: removed files leave the ledger", !ledgerHas("e2e-夹2/"))
	st, d, _ = call("GET", "/v1/fs/trash", nil, nil)
	check("fs: trash lists the item", st == 200 && strings.Contains(string(d), ti.ID), st, string(d))
	st, d, _ = call("POST", "/v1/fs/trash/"+ti.ID+"/restore", nil, nil)
	var rs struct {
		Path          string `json:"path"`
		LedgerUpdated int    `json:"ledger_updated"`
	}
	_ = json.Unmarshal(d, &rs)
	check("fs: restore returns the path and re-adds the ledger row", st == 200 && rs.Path == "e2e-夹2" && rs.LedgerUpdated == 1 && ledgerHas("e2e-夹2/"), st, string(d))
	st, _, _ = call("POST", "/v1/fs/trash/"+ti.ID+"/restore", nil, nil)
	check("fs: second restore → 404", st == 404, st)
	st, _, _ = call("POST", "/v1/fs/trash/not-a-uuid/restore", nil, nil)
	check("fs: forged trash id → 404", st == 404, st)
	st, _, _ = call("POST", "/v1/fs/trash", map[string]any{"path": ""}, nil)
	check("fs: the archive root cannot be deleted", st == 400, st)

	// browser-safety: a foreign web page cannot drive the file manager
	st, _, _ = call("POST", "/v1/fs/trash", map[string]any{"path": "e2e-夹2"}, map[string]string{"Origin": "https://evil.example"})
	check("fs: cross-origin delete refused (403)", st == 403, st)
	st, _, _ = call("POST", "/v1/fs/reveal", map[string]any{"path": ""}, map[string]string{"Origin": "https://evil.example"})
	check("fs: cross-origin reveal refused (403)", st == 403, st)

	// Explorer
	st, _, _ = call("POST", "/v1/fs/reveal", map[string]any{"path": "../../Windows"}, nil)
	check("fs: reveal outside the root refused", st == 400 || st == 501 || st == 403, st)
	if root.Capabilities.Reveal {
		if *revealFlag {
			st, d, _ = call("POST", "/v1/fs/reveal", map[string]any{"path": "e2e-夹2"}, nil)
			check("fs: reveal folder launches Explorer (200)", st == 200, st, string(d))
			st, d, _ = call("POST", "/v1/fs/reveal", map[string]any{"path": "e2e-夹2/" + file.Name}, nil)
			check("fs: reveal file selects it in Explorer (200)", st == 200, st, string(d))
		}
		st, _, _ = call("POST", "/v1/fs/reveal", map[string]any{"path": "不存在的路径"}, nil)
		check("fs: reveal of a missing path → 404", st == 404, st)
	} else {
		st, _, _ = call("POST", "/v1/fs/reveal", map[string]any{"path": ""}, nil)
		check("fs: reveal unavailable here → 501/403", st == 501 || st == 403, st)
	}
}
