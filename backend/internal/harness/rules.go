package harness

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

func containsStr(s, sub string) bool { return strings.Contains(s, sub) }

// Candidates is the rule engine output. Nil fields mean insufficient or
// conflicting evidence; Warnings explain why.
type Candidates struct {
	Department     *string  `json:"department"`
	DocumentType   *string  `json:"document_type"`
	DocumentDate   *string  `json:"document_date"`
	Evidence       []string `json:"evidence"`
	Warnings       []string `json:"warnings"`
	ReviewRequired bool     `json:"review_required"`
}

// engine holds the vocabularies and clock used by the deterministic rules.
type engine struct {
	departments []VocabEntry
	types       []VocabEntry
	now         time.Time
}

type signal struct {
	name   string
	weight int
	strong bool // from filename, title or explicit label (not a body mention)
	ev     string
}

var (
	reDeptLabel = regexp.MustCompile(`(?:发文部门|拟稿部门|申请部门|所属部门|编制部门|制表部门|经办部门|报送部门|部门)\s*[：:]\s*([^\s，,；;。、]{2,12})`)
	reTypeLabel = regexp.MustCompile(`(?:文件类型|文种|文件种类|单据类型|类型)\s*[：:]\s*([^\s，,；;。、]{1,12})`)
	reRawDept   = regexp.MustCompile(`^[\p{Han}]{1,8}(?:部|中心|处|科|室|办公室|局|院)$`)
)

func stem(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func titleLines(text string) []string {
	var out []string
	n := 0
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if n++; n > 3 {
			break
		}
		if utf8.RuneCountInString(l) <= 40 && !strings.ContainsAny(l, "：:") {
			out = append(out, l)
		}
	}
	return out
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// pickField applies the evidence rules for one field.
func pickField(field string, sigs []signal) (*string, []string, string) {
	type agg struct {
		score  int
		strong bool
		evs    []string
	}
	m := map[string]*agg{}
	var order []string
	for _, s := range sigs {
		a := m[s.name]
		if a == nil {
			a = &agg{}
			m[s.name] = a
			order = append(order, s.name)
		}
		a.score += s.weight
		a.strong = a.strong || s.strong
		a.evs = append(a.evs, s.ev)
	}
	if len(order) == 0 {
		return nil, nil, field + "：未发现可用证据"
	}
	sort.SliceStable(order, func(i, j int) bool { return m[order[i]].score > m[order[j]].score })
	top := m[order[0]]
	strongCount := 0
	var allEv []string
	for _, n := range order {
		if m[n].strong {
			strongCount++
		}
		allEv = append(allEv, m[n].evs...)
	}
	if strongCount >= 2 {
		return nil, nil, fmt.Sprintf("%s：信号冲突（%s），需人工审核", field, strings.Join(allEv, "；"))
	}
	if len(order) > 1 && top.score < 2*m[order[1]].score {
		return nil, nil, fmt.Sprintf("%s：信号冲突（%s），需人工审核", field, strings.Join(allEv, "；"))
	}
	if top.score < 2 {
		return nil, nil, field + "：证据不足（仅有单次弱提示）"
	}
	v := order[0]
	evs := make([]string, 0, len(top.evs))
	for _, e := range top.evs {
		evs = append(evs, field+"："+e+"→"+v)
	}
	return &v, evs, ""
}

func (e *engine) vocabSignals(vocab []VocabEntry, filename, text string) (fileSig, bodySig []signal) {
	name := stem(filename)
	for _, v := range vocab {
		for _, k := range append([]string{v.Name}, v.Keywords...) {
			if k != "" && containsStr(name, k) {
				fileSig = append(fileSig, signal{name: v.Name, weight: 2, strong: true, ev: fmt.Sprintf("文件名含“%s”", k)})
				break
			}
		}
		count, first := 0, ""
		seen := map[string]bool{}
		for _, k := range append([]string{v.Name}, v.Keywords...) {
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			if c := strings.Count(text, k); c > 0 {
				count += c
				if first == "" {
					first = k
				}
			}
		}
		if count > 0 {
			w := count
			if w > 2 {
				w = 2
			}
			bodySig = append(bodySig, signal{name: v.Name, weight: w, ev: fmt.Sprintf("正文含“%s”等%d处", first, count)})
		}
	}
	return
}

func (e *engine) department(filename, text string) (*string, []string, string) {
	fs, bs := e.vocabSignals(e.departments, filename, text)
	var explicit []signal
	prefix := false
	// OA file-name convention "部门_标题_日期.ext": a literal organisation name as the first segment is
	// the strongest signal and is kept verbatim (人力资源部 stays 人力资源部, 法务部 needs no vocabulary entry).
	if m := reFilenameDept.FindStringSubmatch(stem(filename)); m != nil && plausibleOrg(m[1]) {
		explicit = append(explicit, signal{name: m[1], weight: 4, strong: true, ev: fmt.Sprintf("文件名开头为“%s”", m[1])})
		prefix = true
	}
	for _, l := range letterheadLines(text) {
		explicit = append(explicit, signal{name: l, weight: 2, strong: true, ev: fmt.Sprintf("正文抬头/落款为“%s”", l)})
	}
	sigs := append(fs, bs...)
	for _, m := range reDeptLabel.FindAllStringSubmatch(text, -1) {
		val := m[1]
		name, ok := val, reRawDept.MatchString(val) && plausibleOrg(val)
		if !ok {
			name, ok = resolveVocab(e.departments, val)
		}
		if ok {
			explicit = append(explicit, signal{name: name, weight: 4, strong: true, ev: fmt.Sprintf("正文含“%s”", strings.Join(strings.Fields(m[0]), ""))})
		}
	}
	// Keyword inference (synonyms such as 报销→财务部) is only a fallback: it must not override an organisation
	// name the document states explicitly, e.g. a 法务部 contract that merely mentions 采购.
	if prefix {
		sigs = explicit // the file-name convention states the department; keyword hits elsewhere are not evidence against it
	} else if len(explicit) > 0 {
		sigs = append(append([]signal{}, fs...), explicit...) // body keywords are weaker than a labelled or letterhead name
	}
	return weakCheck("部门", sigs, pickField)
}

func (e *engine) docType(filename, text string) (*string, []string, string) {
	fs, bs := e.vocabSignals(e.types, filename, text)
	sigs := append(fs, bs...)
	for _, t := range titleLines(text) {
		// The document kind is normally the last word of a title
		// ("关于…计划的通知"), so only the keyword ending last counts.
		bestEnd, bestName, bestKey := -1, "", ""
		for _, v := range e.types {
			for _, k := range append([]string{v.Name}, v.Keywords...) {
				if i := strings.LastIndex(t, k); k != "" && i >= 0 && i+len(k) > bestEnd {
					bestEnd, bestName, bestKey = i+len(k), v.Name, k
				}
			}
		}
		if bestName != "" {
			sigs = append(sigs, signal{name: bestName, weight: 3, strong: true, ev: fmt.Sprintf("正文标题含“%s”", bestKey)})
			break
		}
	}
	for _, m := range reTypeLabel.FindAllStringSubmatch(text, -1) {
		if name, ok := resolveVocab(e.types, m[1]); ok {
			sigs = append(sigs, signal{name: name, weight: 4, strong: true, ev: fmt.Sprintf("正文含“%s”", strings.Join(strings.Fields(m[0]), ""))})
		}
	}
	return weakCheck("类型", sigs, pickField)
}

type dateRes struct {
	iso  string
	ev   string
	warn string
}

func distinct(hs []dateHit) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hs {
		if !seen[h.iso()] {
			seen[h.iso()] = true
			out = append(out, h.iso())
		}
	}
	return out
}

func (e *engine) textDate(text string) *dateRes {
	var all, labeled, alone []dateHit
	for _, h := range findDates(text, e.now) {
		if h.exclude {
			continue
		}
		all = append(all, h)
		if h.label != "" {
			labeled = append(labeled, h)
		}
		if h.alone {
			alone = append(alone, h)
		}
	}
	if d := distinct(labeled); len(d) == 1 {
		h := labeled[0]
		return &dateRes{iso: d[0], ev: fmt.Sprintf("正文含“%s：%s”", h.label, h.disp())}
	} else if len(d) > 1 {
		return &dateRes{warn: "日期：正文含多个不同的带标签日期，无法确定"}
	}
	if len(alone) > 0 {
		h := alone[len(alone)-1]
		return &dateRes{iso: h.iso(), ev: fmt.Sprintf("正文落款日期“%s”", h.disp())}
	}
	if d := distinct(all); len(d) == 1 {
		return &dateRes{iso: d[0], ev: fmt.Sprintf("正文唯一日期“%s”", all[0].disp())}
	} else if len(d) > 1 {
		return &dateRes{warn: "日期：正文含多个无标签日期，无法确定生成日期"}
	}
	return nil
}

func (e *engine) filenameDate(filename string) *dateRes {
	hs := findDates(stem(filename), e.now)
	if d := distinct(hs); len(d) == 1 {
		return &dateRes{iso: d[0], ev: fmt.Sprintf("文件名含“%s”", hs[0].disp())}
	} else if len(d) > 1 {
		return &dateRes{warn: "日期：文件名含多个不同日期，无法确定"}
	}
	return nil
}

func (e *engine) metadataDate(m FileMetadata) *dateRes {
	for _, p := range []struct {
		name string
		v    *string
	}{{"created", m.Created}, {"modified", m.Modified}} {
		if p.v == nil {
			continue
		}
		if hs := findDates(*p.v, e.now); len(hs) > 0 {
			return &dateRes{iso: hs[0].iso(), ev: "文件元数据" + p.name}
		}
	}
	return nil
}

// date resolves the document date. source is a template DateSource or empty.
func (e *engine) date(filename, text string, meta FileMetadata, source string) (*string, []string, string) {
	t, f := e.textDate(text), e.filenameDate(filename)
	var order []*dateRes
	switch source {
	case "filename":
		order = []*dateRes{f, t}
	case "ocr_text":
		order = []*dateRes{t, f}
	case "file_metadata":
		order = []*dateRes{e.metadataDate(meta), t, f}
	default:
		if t != nil && f != nil && t.iso != "" && f.iso != "" && t.iso != f.iso {
			return nil, nil, fmt.Sprintf("日期：文件名与正文日期冲突（%s；%s），需人工审核", f.ev, t.ev)
		}
		order = []*dateRes{t, f}
	}
	var warn string
	for _, r := range order {
		if r == nil {
			continue
		}
		if r.iso != "" {
			v := r.iso
			return &v, []string{"日期：" + r.ev + "→" + r.iso}, ""
		}
		warn = r.warn
	}
	if warn == "" {
		warn = "日期：未发现可用证据"
	}
	return nil, nil, warn
}

// Extract runs the full rule set. tpl (may be nil) seeds department/type.
func (e *engine) Extract(filename, text string, meta FileMetadata, tpl *Template) Candidates {
	var c Candidates
	c.Evidence = []string{}
	c.Warnings = []string{}
	add := func(v *string, ev []string, warn string, dst **string) {
		*dst = v
		c.Evidence = append(c.Evidence, ev...)
		if warn != "" {
			c.Warnings = append(c.Warnings, warn)
		}
	}
	dv, dev, dw := e.department(filename, text)
	tv, tev, tw := e.docType(filename, text)
	// A value inferred only from body keywords is kept as a suggestion but must be confirmed by a person.
	if dv != nil && dw != "" || tv != nil && tw != "" {
		c.ReviewRequired = true
	}
	source := ""
	if tpl != nil {
		source = tpl.DateSource
		why := "模板“" + tpl.Name + "”命中"
		if s := strings.TrimSpace(tpl.Department); s != "" {
			if dv != nil && *dv != s {
				c.Warnings = append(c.Warnings, "部门：规则识别结果与模板预设不一致，需人工审核")
				c.ReviewRequired = true
			}
			dv, dev, dw = &s, []string{"部门：" + why + "→" + s}, ""
		}
		if s := strings.TrimSpace(tpl.DocumentType); s != "" {
			if tv != nil && *tv != s {
				c.Warnings = append(c.Warnings, "类型：规则识别结果与模板预设不一致，需人工审核")
				c.ReviewRequired = true
			}
			tv, tev, tw = &s, []string{"类型：" + why + "→" + s}, ""
		}
	}
	add(dv, dev, dw, &c.Department)
	add(tv, tev, tw, &c.DocumentType)
	gv, gev, gw := e.date(filename, text, meta, source)
	add(gv, gev, gw, &c.DocumentDate)
	if c.Department == nil || c.DocumentType == nil || c.DocumentDate == nil {
		c.ReviewRequired = true
	}
	return c
}

func firstHit(v VocabEntry, s string) string {
	for _, k := range append([]string{v.Name}, v.Keywords...) {
		if k != "" && containsStr(s, k) {
			return k
		}
	}
	return ""
}

var (
	reFilenameDept = regexp.MustCompile(`^([\p{Han}]{2,10}?(?:部|中心|办公室|处|科|局|院))[_\s\-—]`)
	reLetterhead   = regexp.MustCompile(`^([\p{Han}]{2,10}?(?:部|中心|办公室|处|科|局|院))(?:文件|公文|\s*)$`)
)

// letterheadLines returns organisation names that stand alone on one of the first or last few lines
// (letterhead or signature block).
func letterheadLines(text string) []string {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	pick := func(ls []string) (out []string) {
		for _, l := range ls {
			if m := reLetterhead.FindStringSubmatch(l); m != nil && plausibleOrg(m[1]) {
				out = append(out, m[1])
			}
		}
		return
	}
	var res []string
	if len(lines) > 0 {
		head := lines
		if len(head) > 4 {
			head = head[:4]
		}
		res = append(res, pick(head)...)
		tail := lines
		if len(tail) > 4 {
			tail = tail[len(tail)-4:]
		}
		res = append(res, pick(tail)...)
	}
	seen := map[string]bool{}
	var uniq []string
	for _, r := range res {
		if !seen[r] {
			seen[r] = true
			uniq = append(uniq, r)
		}
	}
	return uniq
}

// weakCheck runs pickField and, when the chosen value is backed only by non-strong signals (body keyword
// counts), returns it together with a warning so the caller can require human review.
func weakCheck(field string, sigs []signal, pick func(string, []signal) (*string, []string, string)) (*string, []string, string) {
	v, ev, w := pick(field, sigs)
	if v == nil {
		return v, ev, w
	}
	for _, s := range sigs {
		if s.name == *v && s.strong {
			return v, ev, w
		}
	}
	return v, ev, field + "：仅凭正文关键词推断，证据较弱，需人工确认"
}

// orgLabelWords are words of form labels, not of organisation names. OCR often drops the colon after a label
// ("用人部门：人事部" → "用人部门人事部"), and a merged label+value line must never be read as a department name.
var orgLabelWords = []string{"部门", "单位", "人员", "负责", "经办", "申请", "编制", "拟稿", "发文", "签订", "合同", "日期", "时间"}

func plausibleOrg(s string) bool {
	if utf8.RuneCountInString(s) > 10 {
		return false
	}
	for _, w := range orgLabelWords {
		if strings.Contains(s, w) {
			return false
		}
	}
	return true
}
