package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

var degradedFlag = flagBool("degraded", "import tests/samples/degraded and report recognition/OCR rescue per degradation (needs the main -samples dir)")

// degraded imports every degraded image through the real pipeline and compares the outcome with the labelled
// source document. The hard rule: a file may be wrong or empty, but never *confidently* wrong (review_required=false).
func degraded() {
	var gt struct {
		Files []struct{ File, Department, Type, Date string }
	}
	raw, _ := os.ReadFile(filepath.Join(*dir, "manifest.json"))
	_ = json.Unmarshal(raw, &gt)
	truth := map[string][3]string{}
	for _, f := range gt.Files {
		truth[f.File] = [3]string{f.Department, f.Type, f.Date}
	}
	var dm struct {
		Files []struct{ File, Source, Degradation string }
	}
	raw, err := os.ReadFile(filepath.Join(*dir, "degraded", "manifest.json"))
	if err != nil {
		fmt.Println("no degraded fixtures:", err)
		os.Exit(2)
	}
	_ = json.Unmarshal(raw, &dm)
	var files, paths []string
	meta := map[string]struct{ src, deg string }{}
	for i, f := range dm.Files {
		files = append(files, filepath.Join(*dir, "degraded", f.File))
		name := fmt.Sprintf("d%02d.png", i)
		paths = append(paths, "劣化/"+name)
		meta[name] = struct{ src, deg string }{f.Source, f.Degradation}
	}
	code, d := upload(files, paths, "")
	check("degraded: import accepted", code == 202, code, string(d))
	var j job
	_ = json.Unmarshal(d, &j)
	j = waitFor(j.ID, "awaiting_review")
	fmt.Printf("INFO  task %s status=%s processed=%d/%d success=%d failed=%d\n", j.ID[:8], j.Status, j.ProcessedFiles, j.TotalFiles, j.SuccessCount, j.FailureCount)
	_, d, _ = call("GET", "/v1/files?task_id="+j.ID+"&page_size=200", nil, nil)
	var page struct {
		Items []struct {
			Name   string  `json:"source_name"`
			Dept   *string `json:"detected_department"`
			Type   *string `json:"detected_type"`
			Date   *string `json:"detected_date"`
			Review bool    `json:"review_required"`
			Err    *string `json:"error"`
			OCR    *struct {
				Quality  *string `json:"quality"`
				Profile  string  `json:"profile"`
				Attempts int     `json:"attempts"`
			} `json:"ocr"`
		}
	}
	_ = json.Unmarshal(d, &page)
	sort.Slice(page.Items, func(a, b int) bool { return page.Items[a].Name < page.Items[b].Name })
	full, flagged, empty, confidentWrong, rescued := 0, 0, 0, 0, 0
	for _, it := range page.Items {
		m := meta[it.Name]
		want := truth[m.src]
		got := [3]string{s(it.Dept), s(it.Type), s(it.Date)}
		for i := range got {
			if got[i] == "<nil>" {
				got[i] = ""
			}
		}
		ok := got == want
		anyWrong := false
		for i := range got {
			if got[i] != "" && got[i] != want[i] {
				anyWrong = true
			}
		}
		q, prof, att := "-", "-", 0
		if it.OCR != nil {
			prof, att = it.OCR.Profile, it.OCR.Attempts
			if it.OCR.Quality != nil {
				q = *it.OCR.Quality
			}
		}
		switch {
		case ok && !it.Review:
			full++
		case it.Review:
			flagged++
		}
		if got == [3]string{} {
			empty++
		}
		if anyWrong && !it.Review {
			confidentWrong++
		}
		if att > 1 && ok {
			rescued++
		}
		fmt.Printf("  %-22s %-24s ok=%-5v review=%-5v ocr=%-4s profile=%-9s attempts=%d got=%v\n", m.deg, m.src, ok, it.Review, q, prof, att, got)
	}
	fmt.Printf("INFO  degraded: %d files — fully correct %d, flagged for review %d, empty %d, rescued by retry %d\n", len(page.Items), full, flagged, empty, rescued)
	check("degraded: never confidently wrong", confidentWrong == 0, confidentWrong)
}
