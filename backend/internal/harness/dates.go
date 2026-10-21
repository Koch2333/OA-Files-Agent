package harness

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	reDateCN    = regexp.MustCompile(`(\d{4})\s*年\s*(\d{1,2})\s*月\s*(\d{1,2})\s*[日号]`)
	reDateSep   = regexp.MustCompile(`(\d{4})\s*[-/.]\s*(\d{1,2})\s*[-/.]\s*(\d{1,2})`)
	reDate8     = regexp.MustCompile(`(?:^|[^\d])(\d{8})(?:[^\d]|$)`)
	reDateCNNum = regexp.MustCompile(`([〇零○一二三四五六七八九]{4})年([〇零一二三四五六七八九十]{1,3})月([〇零一二三四五六七八九十]{1,3})[日号]`)

	reDateLabel   = regexp.MustCompile(`(发文日期|成文日期|签发日期|落款日期|制表日期|申请日期|开票日期|生成日期|编制日期|日期|时间)\s*[：:]?\s*$`)
	reDateExclude = regexp.MustCompile(`(出生|有效|截止|到期|起始|开始|结束|交货|付款|入职|离职|生效|终止|截至)(日期|时间)\s*[：:]?\s*$`)
)

type dateHit struct {
	t       time.Time
	raw     string
	start   int
	end     int
	label   string // generation-date label immediately before the date
	alone   bool   // the date is the only content of its line (落款)
	exclude bool   // e.g. 有效日期, not a generation date
}

func (h dateHit) iso() string { return h.t.Format("2006-01-02") }

// validCalendarDate reports whether y-m-d is a real date and plausible
// (1990 .. next calendar year).
func validCalendarDate(y, m, d int, now time.Time) (time.Time, bool) {
	if y < 1990 || y > now.Year()+1 || m < 1 || m > 12 || d < 1 || d > 31 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != m || t.Day() != d {
		return time.Time{}, false
	}
	return t, true
}

// ParseISODate strictly parses YYYY-MM-DD and checks plausibility.
func ParseISODate(s string, now time.Time) (string, bool) {
	if len(s) != 10 {
		return "", false
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return "", false
	}
	if _, ok := validCalendarDate(t.Year(), int(t.Month()), t.Day(), now); !ok {
		return "", false
	}
	return t.Format("2006-01-02"), true
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func cnDigit(r rune) int {
	switch r {
	case '〇', '零', '○':
		return 0
	case '一':
		return 1
	case '二':
		return 2
	case '三':
		return 3
	case '四':
		return 4
	case '五':
		return 5
	case '六':
		return 6
	case '七':
		return 7
	case '八':
		return 8
	case '九':
		return 9
	}
	return -1
}

// parseCNSmall parses 一 .. 三十一 style numerals (also 〇五 digit style).
func parseCNSmall(s string) int {
	rs := []rune(s)
	if i := strings.IndexRune(s, '十'); i >= 0 {
		tens, ones := 1, 0
		if p := []rune(s[:i]); len(p) == 1 {
			tens = cnDigit(p[0])
		} else if len(p) > 1 {
			return -1
		}
		if q := []rune(s[i+len("十"):]); len(q) == 1 {
			ones = cnDigit(q[0])
		} else if len(q) > 1 {
			return -1
		}
		if tens < 0 || ones < 0 {
			return -1
		}
		return tens*10 + ones
	}
	n := 0
	for _, r := range rs {
		d := cnDigit(r)
		if d < 0 {
			return -1
		}
		n = n*10 + d
	}
	return n
}

// findDates returns every valid calendar date in s in textual order.
func findDates(s string, now time.Time) []dateHit {
	var hits []dateHit
	add := func(start, end, y, m, d int) {
		t, ok := validCalendarDate(y, m, d, now)
		if !ok {
			return
		}
		hits = append(hits, dateHit{t: t, raw: s[start:end], start: start, end: end})
	}
	for _, m := range reDateCN.FindAllStringSubmatchIndex(s, -1) {
		add(m[0], m[1], atoi(s[m[2]:m[3]]), atoi(s[m[4]:m[5]]), atoi(s[m[6]:m[7]]))
	}
	for _, m := range reDateSep.FindAllStringSubmatchIndex(s, -1) {
		add(m[0], m[1], atoi(s[m[2]:m[3]]), atoi(s[m[4]:m[5]]), atoi(s[m[6]:m[7]]))
	}
	for _, m := range reDate8.FindAllStringSubmatchIndex(s, -1) {
		v := s[m[2]:m[3]]
		add(m[2], m[3], atoi(v[:4]), atoi(v[4:6]), atoi(v[6:8]))
	}
	for _, m := range reDateCNNum.FindAllStringSubmatchIndex(s, -1) {
		y := 0
		for _, r := range s[m[2]:m[3]] {
			y = y*10 + cnDigit(r)
		}
		add(m[0], m[1], y, parseCNSmall(s[m[4]:m[5]]), parseCNSmall(s[m[6]:m[7]]))
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].start < hits[j].start })
	// Drop overlaps (e.g. 2026-03-15 also matched by another pattern).
	out := hits[:0]
	last := -1
	for _, h := range hits {
		if h.start < last {
			continue
		}
		out = append(out, h)
		last = h.end
	}
	for i := range out {
		h := &out[i]
		ls := strings.LastIndexByte(s[:h.start], '\n') + 1
		le := len(s)
		if k := strings.IndexByte(s[h.end:], '\n'); k >= 0 {
			le = h.end + k
		}
		prefix := s[ls:h.start]
		if len([]rune(prefix)) > 16 {
			pr := []rune(prefix)
			prefix = string(pr[len(pr)-16:])
		}
		if reDateExclude.MatchString(prefix) {
			h.exclude = true
		} else if m := reDateLabel.FindStringSubmatch(prefix); m != nil {
			h.label = m[1]
		}
		if reDateTail.MatchString(s[h.end:le]) {
			h.exclude = true
		}
		line := strings.TrimSpace(s[ls:le])
		line = strings.Trim(line, "。.　 ")
		h.alone = strings.TrimSpace(h.raw) == line
	}
	return out
}

// reDateTail marks dates that are deadlines or effective dates ("自…起",
// "…前完成"), not the document's generation date.
var reDateTail = regexp.MustCompile(`^\s*(起|止|前|至|后|之前|之后|到|截止)`)
