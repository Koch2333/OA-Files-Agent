package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var exampleName = regexp.MustCompile(`^(.+?)_.+_(\d{4}-\d{2}-\d{2})\.\w+$`)

// examples imports a folder of real contest files and reports how the detected department/date compare with
// the `部门_标题_日期.ext` convention in the file names (ground truth for those two fields).
func examples(root string) {
	entries, _ := os.ReadDir(root)
	var files, paths []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), "赛题-") || !exampleName.MatchString(e.Name()) {
			continue
		}
		files = append(files, filepath.Join(root, e.Name()))
		paths = append(paths, "示例/"+displayName(e.Name(), len(files)-1))
	}
	code, d := upload(files, paths, "")
	check("examples import accepted", code == 202, code, string(d))
	var j job
	_ = json.Unmarshal(d, &j)
	j = waitFor(j.ID, "awaiting_review")
	check("examples task awaiting_review", j.Status == "awaiting_review", j.Status)
	_, d, _ = call("GET", "/v1/files?task_id="+j.ID+"&page_size=200", nil, nil)
	var page struct {
		Items []struct {
			Name     string   `json:"source_name"`
			Dept     *string  `json:"detected_department"`
			Type     *string  `json:"detected_type"`
			Date     *string  `json:"detected_date"`
			Review   bool     `json:"review_required"`
			Err      *string  `json:"error"`
			Evidence []string `json:"evidence"`
		} `json:"items"`
	}
	_ = json.Unmarshal(d, &page)
	sort.Slice(page.Items, func(a, b int) bool { return page.Items[a].Name < page.Items[b].Name })
	if *blind {
		for i := range page.Items {
			fmt.Printf("  %s  ←  %s\n", page.Items[i].Name, filepath.Base(files[i]))
		}
		fmt.Println("blind mode: names are neutral, results below are body-only (ground truth = original file name)")
	}
	okDept, okDate := 0, 0
	for _, it := range page.Items {
		m := exampleName.FindStringSubmatch(origName(it.Name, files))
		dept, date := m[1], m[2]
		dOK := it.Dept != nil && *it.Dept == dept
		tOK := it.Date != nil && *it.Date == date
		if dOK {
			okDept++
		}
		if tOK {
			okDate++
		}
		fmt.Printf("  %-70s dept=%-8s type=%-8s date=%-10s review=%v err=%v\n", origName(it.Name, files), s(it.Dept), s(it.Type), s(it.Date), it.Review, it.Err)
		if !dOK || !tOK {
			fmt.Printf("      want dept=%s date=%s evidence=%v\n", dept, date, it.Evidence)
		}
	}
	fmt.Printf("INFO  examples: dept %d/%d, date %d/%d\n", okDept, len(page.Items), okDate, len(page.Items))
	if *blind {
		// Body-only: a wrong department is acceptable only when flagged for review (never confidently wrong).
		confident := 0
		for _, it := range page.Items {
			m := exampleName.FindStringSubmatch(origName(it.Name, files))
			if it.Dept != nil && *it.Dept != m[1] && !it.Review {
				confident++
			}
		}
		check("examples(blind): no confidently-wrong department", confident == 0, confident)
	} else {
		check("examples: department matches filename convention", okDept == len(page.Items), okDept, len(page.Items))
	}
	if !*blind || okDate == len(page.Items) {
	}
	check("examples: date matches filename convention", okDate == len(page.Items), okDate, len(page.Items))
}

var blind = flag.Bool("blind", false, "with -examples: upload under neutral names so only the document body can be used")

func displayName(name string, i int) string {
	if !*blind {
		return name
	}
	return fmt.Sprintf("scan_%02d%s", i, filepath.Ext(name))
}

// origName maps a neutral blind-mode name back to the real file name (ground truth).
func origName(shown string, files []string) string {
	if !*blind {
		return shown
	}
	var i int
	if _, err := fmt.Sscanf(shown, "scan_%d", &i); err == nil && i >= 0 && i < len(files) {
		return filepath.Base(files[i])
	}
	return shown
}
