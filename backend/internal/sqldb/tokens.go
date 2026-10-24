package sqldb

import "strings"

type tokKind int

const (
	tkSpace tokKind = iota
	tkWord          // identifier or keyword
	tkNumber
	tkString // 'text'
	tkQuoted // "ident"
	tkParam  // $n
	tkPunct
	tkComment
)

type token struct {
	kind tokKind
	text string
}

func (t token) is(word string) bool { return t.kind == tkWord && strings.EqualFold(t.text, word) }
func (t token) isP(p string) bool   { return t.kind == tkPunct && t.text == p }

func isWordStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}
func isWordPart(c byte) bool { return isWordStart(c) || c >= '0' && c <= '9' }

// tokenize splits SQL into tokens. String literals and quoted identifiers are kept whole, so rewriting never
// touches their contents.
func tokenize(s string) []token {
	var out []token
	i := 0
	for i < len(s) {
		c := s[i]
		start := i
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
				i++
			}
			out = append(out, token{tkSpace, s[start:i]})
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			out = append(out, token{tkComment, s[start:i]})
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i = min(i+2, len(s))
			out = append(out, token{tkComment, s[start:i]})
		case c == '\'':
			i++
			for i < len(s) {
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			out = append(out, token{tkString, s[start:i]})
		case c == '"':
			i++
			for i < len(s) && s[i] != '"' {
				i++
			}
			i = min(i+1, len(s))
			out = append(out, token{tkQuoted, s[start:i]})
		case c == '$' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			i++
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				i++
			}
			out = append(out, token{tkParam, s[start:i]})
		case c >= '0' && c <= '9':
			for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
				i++
			}
			out = append(out, token{tkNumber, s[start:i]})
		case isWordStart(c):
			for i < len(s) && isWordPart(s[i]) {
				i++
			}
			out = append(out, token{tkWord, s[start:i]})
		case c == ':' && i+1 < len(s) && s[i+1] == ':':
			i += 2
			out = append(out, token{tkPunct, "::"})
		case (c == '<' || c == '>' || c == '!') && i+1 < len(s) && (s[i+1] == '=' || c == '<' && s[i+1] == '>'):
			i += 2
			out = append(out, token{tkPunct, s[start:i]})
		case c == '|' && i+1 < len(s) && s[i+1] == '|':
			i += 2
			out = append(out, token{tkPunct, "||"})
		default:
			i++
			out = append(out, token{tkPunct, s[start:i]})
		}
	}
	return out
}

func join(ts []token) string {
	var b strings.Builder
	for _, t := range ts {
		b.WriteString(t.text)
	}
	return b.String()
}

// sig returns the index of the next non-space, non-comment token at or after i (or len).
func sigFwd(ts []token, i int) int {
	for i < len(ts) && (ts[i].kind == tkSpace || ts[i].kind == tkComment) {
		i++
	}
	return i
}

// sigBack returns the index of the previous non-space token strictly before i (or -1).
func sigBack(ts []token, i int) int {
	i--
	for i >= 0 && (ts[i].kind == tkSpace || ts[i].kind == tkComment) {
		i--
	}
	return i
}

// matchParen returns the index of the ')' matching the '(' at open.
func matchParen(ts []token, open int) int {
	depth := 0
	for i := open; i < len(ts); i++ {
		if ts[i].isP("(") {
			depth++
		} else if ts[i].isP(")") {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTop splits ts[from:to] at top-level commas.
func splitTop(ts []token, from, to int) [][]token {
	var parts [][]token
	depth, start := 0, from
	for i := from; i < to; i++ {
		switch {
		case ts[i].isP("("):
			depth++
		case ts[i].isP(")"):
			depth--
		case ts[i].isP(",") && depth == 0:
			parts = append(parts, ts[start:i])
			start = i + 1
		}
	}
	return append(parts, ts[start:to])
}

func trimSpace(ts []token) []token {
	for len(ts) > 0 && (ts[0].kind == tkSpace || ts[0].kind == tkComment) {
		ts = ts[1:]
	}
	for len(ts) > 0 && (ts[len(ts)-1].kind == tkSpace || ts[len(ts)-1].kind == tkComment) {
		ts = ts[:len(ts)-1]
	}
	return ts
}

// SplitStatements splits a script at top-level semicolons (outside strings, quotes and comments); empty
// statements are dropped. Used to show a DDL preview.
func SplitStatements(script string) []string {
	var out []string
	var cur []token
	flush := func() {
		if s := strings.TrimSpace(join(cur)); s != "" {
			out = append(out, s)
		}
		cur = nil
	}
	for _, t := range tokenize(script) {
		if t.isP(";") {
			flush()
			continue
		}
		cur = append(cur, t)
	}
	flush()
	return out
}
