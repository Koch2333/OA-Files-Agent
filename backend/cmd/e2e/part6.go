package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type previewDoc struct {
	Kind     string            `json:"kind"`
	Name     string            `json:"name"`
	Pages    *int              `json:"pages"`
	PageURL  *string           `json:"page_url"`
	Message  string            `json:"message"`
	Blocks   []json.RawMessage `json:"blocks"`
	Sheets   []json.RawMessage `json:"sheets"`
	Warnings []string          `json:"warnings"`
}

func getPNG(path string) (int, []byte, http.Header) {
	res, err := http.Get(*base + path)
	if err != nil {
		return 0, nil, nil
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b, res.Header
}

// part6: document preview of imported originals and archived files, plus the OCR summary on file records.
func part6() {
	println("--- part 6: document preview and OCR summary ---")
	st, d, _ := call("GET", "/v1/files?page_size=200", nil, nil)
	var all struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"source_name"`
			Task string `json:"task_id"`
			OCR  *struct {
				Used     bool    `json:"used"`
				Quality  *string `json:"quality"`
				Profile  string  `json:"profile"`
				Attempts int     `json:"attempts"`
			} `json:"ocr"`
			Archive string  `json:"archive_status"`
			Err     *string `json:"error"`
		}
	}
	_ = json.Unmarshal(d, &all)
	check("preview: file listing available", st == 200 && len(all.Items) > 0, st)
	byName := func(n string) (string, bool) {
		for _, it := range all.Items {
			if it.Name == n && it.Err == nil {
				return it.ID, true
			}
		}
		return "", false
	}
	cases := []struct{ name, kind string }{
		{"财务部_2026年3月报销申请.docx", "document"},
		{"采购部_2026年Q1设备采购报表.xlsx", "spreadsheet"},
		{"办公电脑采购合同.pdf", "pdf"},
		{"采购申请单_拍照.png", "image"},
	}
	for _, c := range cases {
		id, ok := byName(c.name)
		if !ok {
			check("preview: sample present "+c.name, false)
			continue
		}
		st, d, _ = call("GET", "/v1/files/"+id+"/preview", nil, nil)
		var p previewDoc
		_ = json.Unmarshal(d, &p)
		okKind := st == 200 && p.Kind == c.kind && p.Name == c.name
		switch c.kind {
		case "document":
			okKind = okKind && len(p.Blocks) > 0
		case "spreadsheet":
			okKind = okKind && len(p.Sheets) > 0
		case "pdf", "image":
			okKind = okKind && p.Pages != nil && *p.Pages >= 1 && p.PageURL != nil && strings.Contains(*p.PageURL, "{page}")
		}
		check("preview: "+c.name+" → "+c.kind, okKind, st, string(d))
		if (c.kind == "pdf" || c.kind == "image") && p.PageURL != nil {
			u := strings.ReplaceAll(*p.PageURL, "{page}", "1")
			code, b, h := getPNG(u)
			check("preview: page 1 of "+c.name+" is a PNG (no-store)", code == 200 && len(b) > 100 && bytes.HasPrefix(b, []byte("\x89PNG")) && h.Get("Cache-Control") == "no-store", code, len(b))
			code, _, _ = getPNG(strings.ReplaceAll(*p.PageURL, "{page}", "999"))
			check("preview: page beyond the end → 400", code == 400, code)
		}
		if c.kind == "pdf" {
			code, _, _ := getPNG("/v1/files/" + id + "/preview/page?page=0")
			check("preview: page=0 → 400", code == 400, code)
			code, _, _ = getPNG("/v1/files/" + id + "/preview/page?max_side=64")
			check("preview: max_side below 256 → 400", code == 400, code)
		}
	}
	st, _, _ = call("GET", "/v1/files/00000000-0000-4000-8000-0000000000aa/preview", nil, nil)
	check("preview: unknown file → 404", st == 404, st)
	if id, ok := byName("加密合同.pdf"); ok {
		st, _, _ = call("GET", "/v1/files/"+id+"/preview", nil, nil)
		check("preview: encrypted original → 422", st == 422, st)
	}

	// archived files (the worker must be allowed to read the archive root)
	if f, ok := firstFile("", 4); ok {
		st, d, _ = call("GET", "/v1/fs/preview?path="+url.QueryEscape(f.Path), nil, nil)
		var p previewDoc
		_ = json.Unmarshal(d, &p)
		check("preview: archived file previews (200)", st == 200 && p.Kind != "" && p.Kind != "unsupported", st, string(d))
		if p.PageURL != nil {
			code, b, _ := getPNG(strings.ReplaceAll(*p.PageURL, "{page}", "1"))
			check("preview: archived page image is a PNG", code == 200 && bytes.HasPrefix(b, []byte("\x89PNG")), code)
		}
	}
	for _, bad := range []string{"../../etc/passwd", ".journal/x", "a/../../b"} {
		st, _, _ = call("GET", "/v1/fs/preview?path="+url.QueryEscape(bad), nil, nil)
		check("preview: archived preview refuses "+bad, st == 400 || st == 404, st)
		code, _, _ := getPNG("/v1/fs/preview/page?path=" + url.QueryEscape(bad))
		check("preview: archived page refuses "+bad, code == 400 || code == 404, code)
	}
	st, _, _ = call("GET", "/v1/fs/preview?path="+url.QueryEscape("不存在.pdf"), nil, nil)
	check("preview: missing archived file → 404", st == 404, st)

	// OCR summary on file records
	docxID, _ := byName("财务部_2026年3月报销申请.docx")
	hasOCR, docOCR := 0, false
	for _, it := range all.Items {
		if it.OCR != nil && it.OCR.Used {
			hasOCR++
			check("ocr: quality is good/fair/poor for "+it.Name, it.OCR.Quality != nil && (*it.OCR.Quality == "good" || *it.OCR.Quality == "fair" || *it.OCR.Quality == "poor") && it.OCR.Attempts >= 1 && it.OCR.Profile != "", it.OCR)
		}
		if it.ID == docxID && it.OCR == nil {
			docOCR = true
		}
	}
	check("ocr: scanned/photo files carry an OCR summary", hasOCR >= 2, hasOCR)
	check("ocr: native Word files have ocr = null", docOCR)
}
