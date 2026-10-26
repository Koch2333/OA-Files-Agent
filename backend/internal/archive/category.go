package archive

import (
	"errors"
	"sort"
	"strings"
)

// CategoryOther is the reserved built-in department/type value "其他" (other). It is a normal, explicit
// value for the naming templates ({department}/{type} render it verbatim, e.g. 其他/2026/…); it is never
// assigned by the AI on its own. Read paths (ledger, export) additionally show empty/unknown values as
// 其他 (DisplayCategory) without rewriting stored data.
const CategoryOther = "其他"

// maxCategoryRunes is the length limit of a department/type value.
const maxCategoryRunes = 60

// ValidateCategory checks one manually entered department/type value (already trimmed by the caller or not).
// Empty values are valid here: they mean "clear the field" and are handled by the caller.
func ValidateCategory(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if runeLen(s) > maxCategoryRunes {
		return errors.New("部门/类型过长（最多 60 个字符）")
	}
	for _, r := range s {
		if illegalRune(r) {
			return errors.New("部门/类型不能包含 \\ / : * ? \" < > | 或控制字符")
		}
	}
	if strings.Trim(s, ". ") == "" {
		return errors.New("部门/类型不能只由点号组成")
	}
	return nil
}

// IsUnknownCategory reports whether a stored department/type value means "not classified":
// empty or the review placeholder 待审核 (其他 itself is a real value, not "unknown").
func IsUnknownCategory(s string) bool {
	switch strings.TrimSpace(nfc(s)) {
	case "", fallbackReview:
		return true
	}
	return false
}

// UnknownCategoryValues are the stored values that the ledger filter "其他" matches in addition to 其他.
func UnknownCategoryValues() []string { return []string{"", fallbackReview} }

// DisplayCategory maps a stored department/type value to what readers see: empty and placeholder values
// become 其他, everything else is returned unchanged.
func DisplayCategory(s string) string {
	if IsUnknownCategory(s) {
		return CategoryOther
	}
	return s
}

// CategoryKey is the de-duplication key of a department/type value: NFC, trimmed, case-insensitive.
func CategoryKey(s string) string { return strings.ToLower(strings.TrimSpace(nfc(s))) }

// CategorySource tells where an option came from.
const (
	CategorySourceBuiltin  = "builtin"  // built-in vocabulary
	CategorySourceLedger   = "ledger"   // already used in the ledger
	CategorySourcePlan     = "plan"     // present in the current task's plan
	CategorySourceReserved = "reserved" // 其他
)

// CategoryOption is one suggestion for the manual department/type fields.
type CategoryOption struct {
	Value  string `json:"value"`
	Source string `json:"source"`
	Count  int    `json:"count"`
}

// UsedValue is a value with its usage count.
type UsedValue struct {
	Value string
	Count int
}

// MaxCategoryOptions caps the list returned to the UI.
const MaxCategoryOptions = 200

// MergeCategoryOptions merges built-in values (in their own order), ledger usage (most used first) and the
// current plan's values into one de-duplicated list; 其他 is always last and is never counted away by the
// cap. Counts of equal keys are summed; the first spelling wins. Empty/placeholder values are skipped.
func MergeCategoryOptions(builtin []string, ledger, plan []UsedValue, limit int) []CategoryOption {
	if limit <= 0 || limit > MaxCategoryOptions {
		limit = MaxCategoryOptions
	}
	idx := map[string]int{}
	var out []CategoryOption
	add := func(v, src string, n int) {
		v = strings.TrimSpace(nfc(v))
		if v == "" || IsUnknownCategory(v) || ValidateCategory(v) != nil {
			return
		}
		k := CategoryKey(v)
		if k == CategoryKey(CategoryOther) {
			return
		}
		if i, ok := idx[k]; ok {
			out[i].Count += n
			return
		}
		idx[k] = len(out)
		out = append(out, CategoryOption{Value: v, Source: src, Count: n})
	}
	for _, v := range builtin {
		add(v, CategorySourceBuiltin, 0)
	}
	l := append([]UsedValue(nil), ledger...)
	sort.SliceStable(l, func(i, j int) bool { return l[i].Count > l[j].Count })
	for _, u := range l {
		add(u.Value, CategorySourceLedger, u.Count)
	}
	for _, u := range plan {
		add(u.Value, CategorySourcePlan, u.Count)
	}
	if len(out) > limit-1 {
		out = out[:limit-1]
	}
	return append(out, CategoryOption{Value: CategoryOther, Source: CategorySourceReserved})
}
