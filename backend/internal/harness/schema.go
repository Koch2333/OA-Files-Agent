package harness

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"unicode/utf8"
)

// validateSchema checks a decoded JSON value against the small JSON Schema
// subset used by tools: type (string or list), properties, required,
// additionalProperties:false, enum, minLength, maxLength, pattern, minimum,
// maximum, items, maxItems. Error messages name the path but never echo the
// offending value (it may be document-derived).
func validateSchema(schema map[string]any, v any, path string) error {
	if t, ok := schema["type"]; ok {
		if !typeMatches(t, v) {
			return fmt.Errorf("%s: wrong type", path)
		}
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if e == v {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s: value not allowed", path)
		}
	}
	switch x := v.(type) {
	case string:
		n := utf8.RuneCountInString(x)
		if m, ok := num(schema["minLength"]); ok && float64(n) < m {
			return fmt.Errorf("%s: too short", path)
		}
		if m, ok := num(schema["maxLength"]); ok && float64(n) > m {
			return fmt.Errorf("%s: too long", path)
		}
		if p, ok := schema["pattern"].(string); ok {
			if re, err := regexp.Compile(p); err == nil && !re.MatchString(x) {
				return fmt.Errorf("%s: does not match pattern", path)
			}
		}
	case float64:
		if m, ok := num(schema["minimum"]); ok && x < m {
			return fmt.Errorf("%s: below minimum", path)
		}
		if m, ok := num(schema["maximum"]); ok && x > m {
			return fmt.Errorf("%s: above maximum", path)
		}
	case []any:
		if m, ok := num(schema["maxItems"]); ok && float64(len(x)) > m {
			return fmt.Errorf("%s: too many items", path)
		}
		if is, ok := schema["items"].(map[string]any); ok {
			for i, e := range x {
				if err := validateSchema(is, e, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		if req, ok := schema["required"].([]string); ok {
			for _, r := range req {
				if _, present := x[r]; !present {
					return fmt.Errorf("%s.%s: required", path, r)
				}
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ps, known := props[k].(map[string]any)
			if !known {
				if ap, ok := schema["additionalProperties"].(bool); ok && !ap {
					return fmt.Errorf("%s: unknown property", path)
				}
				continue
			}
			if err := validateSchema(ps, x[k], path+"."+k); err != nil {
				return err
			}
		}
	}
	return nil
}

func num(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func typeMatches(t any, v any) bool {
	switch tt := t.(type) {
	case string:
		return oneType(tt, v)
	case []string:
		for _, s := range tt {
			if oneType(s, v) {
				return true
			}
		}
	}
	return false
}

func oneType(t string, v any) bool {
	switch t {
	case "null":
		return v == nil
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	case "array":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	}
	return false
}
