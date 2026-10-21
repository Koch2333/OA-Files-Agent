// Package archive renders archive names/directories deterministically, plans
// conflict-free destinations and publishes files with staging, hash
// verification, journaling and safe rollback. Standard library only; no LLM.
package archive

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
)

const (
	maxSegmentRunes = 120
	maxTotalRunes   = 240

	fallbackReview = "待审核"
	fallbackDate   = "未知日期"
	fallbackName   = "未命名"
)

// Rule holds the naming and directory templates. Placeholders:
// {date}{year}{month}{day}{department}{type}{original}{stem}{ext}.
type Rule struct{ NamingPattern, DirectoryPattern string }

// Meta carries the recognised metadata; nil or empty means "not known".
type Meta struct{ Department, DocumentType, Date *string }

// Render returns the final file name and the relative directory (forward
// slashes, "" meaning the archive root). Missing metadata falls back to a
// visible placeholder and is reported in warnings; nothing is invented.
func Render(rule Rule, original string, m Meta) (name, dir string, warnings []string, err error) {
	base := original
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = nfc(base)
	ext := sanitizeExt(path.Ext(base))
	stem := strings.TrimSuffix(base, path.Ext(base))

	naming := rule.NamingPattern
	if naming == "" {
		naming = "{original}"
	}
	if strings.ContainsAny(naming, `/\`) {
		return "", "", nil, errors.New("命名模板不能包含路径分隔符")
	}

	ctx := &renderCtx{meta: m, original: base, stem: stem, ext: ext}
	rendered, err := ctx.expand(naming)
	if err != nil {
		return "", "", nil, err
	}
	body := rendered
	if ext != "" && strings.HasSuffix(strings.ToLower(body), strings.ToLower(ext)) {
		body = body[:len(body)-len(ext)]
	}
	body = sanitizeSegment(body, maxSegmentRunes-runeLen(ext))
	if body == "" {
		body = fallbackName
		ctx.warn("命名模板渲染为空，已使用“" + fallbackName + "”")
	}
	name = body + ext

	if rule.DirectoryPattern != "" {
		parts := strings.FieldsFunc(rule.DirectoryPattern, func(r rune) bool { return r == '/' || r == '\\' })
		var segs []string
		for _, p := range parts {
			r, e := ctx.expand(p)
			if e != nil {
				return "", "", nil, e
			}
			if s := sanitizeSegment(r, maxSegmentRunes); s != "" {
				segs = append(segs, s)
			}
		}
		dir = strings.Join(segs, "/")
	}

	// Enforce the total length cap by shortening the stem, never the extension.
	for runeLen(joinRel(dir, name)) > maxTotalRunes {
		r := []rune(strings.TrimSuffix(name, ext))
		if len(r) <= 8 {
			return "", "", nil, errors.New("目标路径过长")
		}
		over := runeLen(joinRel(dir, name)) - maxTotalRunes
		cut := over
		if cut > len(r)-8 {
			cut = len(r) - 8
		}
		name = strings.TrimRight(string(r[:len(r)-cut]), ". ") + ext
		ctx.warn("目标路径过长，文件名已截断")
	}
	if err = ValidateRelative(joinRel(dir, name)); err != nil {
		return "", "", nil, fmt.Errorf("渲染结果无效: %w", err)
	}
	return name, dir, ctx.warnings, nil
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func runeLen(s string) int { return len([]rune(s)) }

type renderCtx struct {
	meta                Meta
	original, stem, ext string
	warnings            []string
}

func (c *renderCtx) warn(w string) {
	for _, e := range c.warnings {
		if e == w {
			return
		}
	}
	c.warnings = append(c.warnings, w)
}

func (c *renderCtx) expand(pattern string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(pattern); {
		ch := pattern[i]
		switch ch {
		case '}':
			return "", errors.New("模板包含未配对的 }")
		case '{':
			end := strings.IndexByte(pattern[i:], '}')
			if end < 0 {
				return "", errors.New("模板包含未配对的 {")
			}
			key := pattern[i+1 : i+end]
			v, err := c.value(key)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i += end + 1
		default:
			b.WriteByte(ch)
			i++
		}
	}
	return b.String(), nil
}

func (c *renderCtx) value(key string) (string, error) {
	switch key {
	case "original":
		return cleanValue(c.original), nil
	case "stem":
		return cleanValue(c.stem), nil
	case "ext":
		return c.ext, nil
	case "department":
		return c.text(c.meta.Department, "部门"), nil
	case "type":
		return c.text(c.meta.DocumentType, "文件类型"), nil
	case "date", "year", "month", "day":
		t, ok := c.date()
		if !ok {
			return fallbackDate, nil
		}
		switch key {
		case "year":
			return t.Format("2006"), nil
		case "month":
			return t.Format("01"), nil
		case "day":
			return t.Format("02"), nil
		}
		return t.Format("2006-01-02"), nil
	}
	return "", fmt.Errorf("未知占位符 {%s}", key)
}

func (c *renderCtx) text(p *string, label string) string {
	if p != nil {
		if v := cleanValue(strings.TrimSpace(*p)); v != "" {
			return v
		}
	}
	c.warn(fmt.Sprintf("未识别到%s，已使用“%s”占位", label, fallbackReview))
	return fallbackReview
}

func (c *renderCtx) date() (time.Time, bool) {
	if c.meta.Date != nil {
		if t, err := time.Parse("2006-01-02", strings.TrimSpace(*c.meta.Date)); err == nil {
			return t, true
		}
		c.warn("日期格式无效（需 YYYY-MM-DD），已使用“" + fallbackDate + "”占位")
		return time.Time{}, false
	}
	c.warn("未识别到日期，已使用“" + fallbackDate + "”占位")
	return time.Time{}, false
}

func illegalRune(r rune) bool {
	return strings.ContainsRune(`\/:*?"<>|`, r) || unicode.IsControl(r) || r == 0xFFFD ||
		(r >= 0x200B && r <= 0x200F) || (r >= 0x202A && r <= 0x202E) || r == 0xFEFF
}

// cleanValue strips Windows-illegal characters from one substituted value.
func cleanValue(s string) string {
	s = nfc(s)
	var b strings.Builder
	for _, r := range s {
		if !illegalRune(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sanitizeExt(ext string) string {
	ext = cleanValue(ext)
	ext = strings.TrimRight(ext, ". ")
	if ext == "." || strings.ContainsAny(ext, " ") {
		return ""
	}
	if runeLen(ext) > 16 {
		return ""
	}
	return ext
}

func reservedName(seg string) bool {
	stem := strings.ToUpper(strings.SplitN(seg, ".", 2)[0])
	stem = strings.TrimRight(stem, " ")
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9'
}

// sanitizeSegment makes one path component safe on Windows. It returns "" when
// nothing usable remains.
func sanitizeSegment(s string, limit int) string {
	s = cleanValue(s)
	s = strings.Trim(s, ". \t")
	if limit < 1 {
		limit = 1
	}
	if r := []rune(s); len(r) > limit {
		s = strings.Trim(string(r[:limit]), ". ")
	}
	if s != "" && reservedName(s) {
		s = "_" + s
	}
	return s
}

// ValidateRelative rejects anything that is not a clean, relative,
// forward-slash path (absolute, drive letter, UNC, "..", backslashes, ...).
func ValidateRelative(p string) error {
	if p == "" {
		return errors.New("empty path")
	}
	if runeLen(p) > maxTotalRunes {
		return errors.New("path too long")
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return errors.New("absolute path")
	}
	if strings.ContainsAny(p, "\\:<>\"|?*\x00") {
		return errors.New("illegal character")
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return errors.New("control character")
		}
	}
	if path.Clean(p) != p {
		return errors.New("path is not clean")
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." {
			return errors.New("invalid path component")
		}
		if strings.TrimRight(c, ". ") != c || strings.TrimSpace(c) != c {
			return errors.New("invalid path component")
		}
		if runeLen(c) > maxSegmentRunes {
			return errors.New("path component too long")
		}
		if reservedName(c) {
			return errors.New("reserved filename")
		}
	}
	return nil
}
