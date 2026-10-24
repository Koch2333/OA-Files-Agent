package sqldb

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// The application writes its SQL once, in a restricted PostgreSQL-flavoured subset ($n placeholders, ::casts, now(),
// ILIKE, LIMIT/OFFSET, FOR UPDATE [SKIP LOCKED], ON CONFLICT, true/false, to_char(d,'YYYY-MM-DD'), substr, concat,
// gen_random_uuid()). translate rewrites exactly those constructs for MySQL and SQL Server; for PostgreSQL the text
// is passed through (only ILIKE gets an explicit ESCAPE). Anything outside the subset is passed through unchanged,
// so the integration test matrix is what proves a new query works on every engine.

// LikeEscape is the escape character every LIKE pattern built by the application uses.
const LikeEscape = '!'

// EscapeLike escapes %, _ and the escape character itself so s matches literally inside a LIKE pattern.
func EscapeLike(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}

type translated struct {
	sql    string
	argIdx []int // MySQL: original 0-based argument index for each '?'
}

type cacheKey struct {
	p Provider
	q string
}

var (
	tcache      sync.Map
	tcacheCount atomic.Int64
)

func translate(p Provider, q string) translated {
	k := cacheKey{p, q}
	if v, ok := tcache.Load(k); ok {
		return v.(translated)
	}
	t := doTranslate(p, q)
	if n := tcacheCount.Add(1); n < 4096 {
		tcache.Store(k, t)
	}
	return t
}

func doTranslate(p Provider, q string) translated {
	ts := tokenize(q)
	ts = rewriteILike(p, ts)
	if p == Postgres {
		return translated{sql: join(ts)}
	}
	ts = stripCasts(ts)
	ts = rewriteFuncs(p, ts)
	ts = rewriteOnConflict(p, ts)
	if p == MSSQL {
		ts = rewriteForUpdateMSSQL(ts)
		ts = rewriteLimitMSSQL(ts)
	}
	return placeholders(p, ts)
}

func stripCasts(ts []token) []token {
	out := make([]token, 0, len(ts))
	for i := 0; i < len(ts); i++ {
		if ts[i].isP("::") && i+1 < len(ts) && ts[i+1].kind == tkWord {
			i++
			for i+2 < len(ts) && ts[i+1].isP("[") && ts[i+2].isP("]") {
				i += 2
			}
			continue
		}
		out = append(out, ts[i])
	}
	return out
}

func word(s string) token  { return token{tkWord, s} }
func punct(s string) token { return token{tkPunct, s} }
func sp() token            { return token{tkSpace, " "} }
func raw(s string) token   { return token{tkWord, s} }

func rewriteFuncs(p Provider, ts []token) []token {
	out := make([]token, 0, len(ts))
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		if t.kind != tkWord {
			out = append(out, t)
			continue
		}
		if p == MSSQL && t.is("at") {
			pb := sigBack(ts, i)
			if n := sigFwd(ts, i+1); !(n < len(ts) && ts[n].is("TIME")) && !(pb >= 0 && ts[pb].isP("[")) {
				out = append(out, raw("[at]"))
				continue
			}
		}
		prev := sigBack(ts, i)
		if prev >= 0 && ts[prev].isP(".") {
			out = append(out, t)
			continue
		}
		next := sigFwd(ts, i+1)
		callable := next < len(ts) && ts[next].isP("(")
		switch {
		case t.is("now") && callable && ts[sigFwd(ts, next+1)].isP(")"):
			i = sigFwd(ts, next+1)
			if p == MySQL {
				out = append(out, raw("UTC_TIMESTAMP(6)"))
			} else {
				out = append(out, raw("SYSUTCDATETIME()"))
			}
		case t.is("gen_random_uuid") && callable && ts[sigFwd(ts, next+1)].isP(")"):
			i = sigFwd(ts, next+1)
			if p == MySQL {
				out = append(out, raw("UUID()"))
			} else {
				out = append(out, raw("LOWER(CONVERT(CHAR(36), NEWID()))"))
			}
		case t.is("to_char") && callable:
			end := matchParen(ts, next)
			args := splitTop(ts, next+1, end)
			if end > 0 && len(args) == 2 && strings.EqualFold(strings.TrimSpace(join(args[1])), "'YYYY-MM-DD'") {
				i = end
				a := join(trimSpace(args[0]))
				if p == MySQL {
					out = append(out, raw("DATE_FORMAT("+a+", '%Y-%m-%d')"))
				} else {
					out = append(out, raw("CONVERT(CHAR(10), "+a+", 23)"))
				}
				continue
			}
			out = append(out, t)
		case p == MSSQL && t.is("substr") && callable:
			out = append(out, raw("SUBSTRING"))
		case p == MSSQL && t.is("true") && !(next < len(ts) && ts[next].isP("(")):
			out = append(out, raw("1"))
		case p == MSSQL && t.is("false") && !(next < len(ts) && ts[next].isP("(")):
			out = append(out, raw("0"))
		default:
			out = append(out, t)
		}
	}
	return out
}

func rewriteILike(p Provider, ts []token) []token {
	out := make([]token, 0, len(ts)+8)
	for i := 0; i < len(ts); i++ {
		if !ts[i].is("ILIKE") {
			out = append(out, ts[i])
			continue
		}
		// right operand: a parameter or string literal
		r := sigFwd(ts, i+1)
		if r >= len(ts) || (ts[r].kind != tkParam && ts[r].kind != tkString) {
			out = append(out, ts[i])
			continue
		}
		esc := []token{sp(), word("ESCAPE"), sp(), {tkString, "'" + string(LikeEscape) + "'"}}
		if p == Postgres {
			out = append(out, ts[i], sp(), ts[r])
			out = append(out, esc...)
			i = r
			continue
		}
		// pop the left operand (word ('.' word)*) from out
		for len(out) > 0 && (out[len(out)-1].kind == tkSpace) {
			out = out[:len(out)-1]
		}
		j := len(out)
		for j > 0 && (out[j-1].kind == tkWord || out[j-1].kind == tkQuoted) {
			j--
			if j > 0 && out[j-1].isP(".") {
				j--
				continue
			}
			break
		}
		left := append([]token(nil), out[j:]...)
		out = out[:j]
		out = append(out, word("LOWER"), punct("("))
		out = append(out, left...)
		out = append(out, punct(")"), sp(), word("LIKE"), sp(), word("LOWER"), punct("("), ts[r], punct(")"))
		out = append(out, esc...)
		i = r
	}
	return out
}

// ownerSelect finds the SELECT that owns the clause at index at (same parenthesis depth, searching backwards).
func ownerSelect(ts []token, at int) int {
	depth := 0
	for p := at - 1; p >= 0; p-- {
		switch {
		case ts[p].isP(")"):
			depth++
		case ts[p].isP("("):
			if depth == 0 {
				return -1
			}
			depth--
		case depth == 0 && ts[p].is("SELECT"):
			return p
		}
	}
	return -1
}

func rewriteLimitMSSQL(ts []token) []token {
	for i := len(ts) - 1; i >= 0; i-- {
		if !ts[i].is("LIMIT") {
			continue
		}
		j := sigFwd(ts, i+1)
		if j >= len(ts) || (ts[j].kind != tkParam && ts[j].kind != tkNumber) {
			continue
		}
		lim := ts[j]
		end := j + 1
		var off *token
		if k := sigFwd(ts, j+1); k < len(ts) && ts[k].is("OFFSET") {
			if m := sigFwd(ts, k+1); m < len(ts) && (ts[m].kind == tkParam || ts[m].kind == tkNumber) {
				o := ts[m]
				off = &o
				end = m + 1
			}
		}
		s := ownerSelect(ts, i)
		if s < 0 {
			continue
		}
		// trim whitespace before LIMIT
		start := i
		for start > 0 && ts[start-1].kind == tkSpace {
			start--
		}
		if off == nil || (off.kind == tkNumber && off.text == "0") {
			ins := s + 1
			if d := sigFwd(ts, s+1); d < len(ts) && ts[d].is("DISTINCT") {
				ins = d + 1
			}
			rest := append([]token(nil), ts[end:]...)
			head := append([]token(nil), ts[:start]...)
			head = append(head[:ins], append([]token{sp(), word("TOP"), sp(), punct("("), lim, punct(")")}, head[ins:]...)...)
			ts = append(head, rest...)
			i = len(ts)
			continue
		}
		hasOrder := false
		depth := 0
		for p := s + 1; p < i; p++ {
			switch {
			case ts[p].isP("("):
				depth++
			case ts[p].isP(")"):
				depth--
			case depth == 0 && ts[p].is("ORDER"):
				hasOrder = true
			}
		}
		repl := []token{}
		if !hasOrder {
			repl = append(repl, sp(), word("ORDER"), sp(), word("BY"), sp(), raw("(SELECT NULL)"))
		}
		repl = append(repl, sp(), word("OFFSET"), sp(), *off, sp(), word("ROWS"), sp(), word("FETCH"), sp(), word("NEXT"), sp(), lim, sp(), word("ROWS"), sp(), word("ONLY"))
		rest := append([]token(nil), ts[end:]...)
		head := append([]token(nil), ts[:start]...)
		ts = append(append(head, repl...), rest...)
		i = len(ts)
	}
	return ts
}

var aliasStop = map[string]bool{"WHERE": true, "JOIN": true, "INNER": true, "LEFT": true, "RIGHT": true, "FULL": true, "CROSS": true, "ORDER": true,
	"GROUP": true, "LIMIT": true, "ON": true, "FOR": true, "UNION": true, "OFFSET": true, "SET": true, "HAVING": true, "OUTER": true}

func rewriteForUpdateMSSQL(ts []token) []token {
	for i := 0; i < len(ts); i++ {
		if !ts[i].is("FOR") {
			continue
		}
		u := sigFwd(ts, i+1)
		if u >= len(ts) || !ts[u].is("UPDATE") {
			continue
		}
		end := u + 1
		ofAlias := ""
		skip := false
		for {
			k := sigFwd(ts, end)
			switch {
			case k < len(ts) && ts[k].is("OF"):
				if a := sigFwd(ts, k+1); a < len(ts) && ts[a].kind == tkWord {
					ofAlias = ts[a].text
					end = a + 1
					continue
				}
			case k < len(ts) && ts[k].is("SKIP"):
				if a := sigFwd(ts, k+1); a < len(ts) && ts[a].is("LOCKED") {
					skip = true
					end = a + 1
					continue
				}
			case k < len(ts) && ts[k].is("NOWAIT"):
				end = k + 1
				continue
			}
			break
		}
		s := ownerSelect(ts, i)
		if s < 0 {
			continue
		}
		// first FROM at depth 0 of this select
		from, depth := -1, 0
		for p := s + 1; p < i; p++ {
			switch {
			case ts[p].isP("("):
				depth++
			case ts[p].isP(")"):
				depth--
			case depth == 0 && ts[p].is("FROM"):
				from = p
			}
			if from >= 0 {
				break
			}
		}
		if from < 0 {
			continue
		}
		// table reference list: name [AS] [alias] possibly followed by JOINs; remember the insert position
		hintAt := -1
		refAfter := func(nameIdx int) int { // returns index after name+alias
			pos := nameIdx
			n := sigFwd(ts, nameIdx+1)
			if n < len(ts) && ts[n].is("AS") {
				n = sigFwd(ts, n+1)
			}
			if n < len(ts) && ts[n].kind == tkWord && !aliasStop[strings.ToUpper(ts[n].text)] {
				pos = n
			}
			return pos
		}
		first := sigFwd(ts, from+1)
		if ofAlias == "" {
			hintAt = refAfter(first)
		} else {
			cand := []int{first}
			d2 := 0
			for p := first; p < i; p++ {
				switch {
				case ts[p].isP("("):
					d2++
				case ts[p].isP(")"):
					d2--
				case d2 == 0 && ts[p].is("JOIN"):
					cand = append(cand, sigFwd(ts, p+1))
				}
			}
			for _, c := range cand {
				if c >= len(ts) {
					continue
				}
				pos := refAfter(c)
				if strings.EqualFold(ts[pos].text, ofAlias) {
					hintAt = pos
					break
				}
			}
			if hintAt < 0 {
				hintAt = refAfter(first)
			}
		}
		hint := " WITH (UPDLOCK, ROWLOCK)"
		if skip {
			hint = " WITH (UPDLOCK, ROWLOCK, READPAST)"
		}
		start := i
		for start > 0 && ts[start-1].kind == tkSpace {
			start--
		}
		rest := append([]token(nil), ts[end:]...)
		head := append([]token(nil), ts[:start]...)
		head = append(head[:hintAt+1], append([]token{raw(hint)}, head[hintAt+1:]...)...)
		ts = append(head, rest...)
		i = hintAt
	}
	return ts
}

func rewriteOnConflict(p Provider, ts []token) []token {
	oc := -1
	for i := 0; i+1 < len(ts); i++ {
		if ts[i].is("ON") {
			if c := sigFwd(ts, i+1); c < len(ts) && ts[c].is("CONFLICT") {
				oc = i
				break
			}
		}
	}
	if oc < 0 {
		return ts
	}
	c := sigFwd(ts, oc+1)
	o := sigFwd(ts, c+1)
	if o >= len(ts) || !ts[o].isP("(") {
		panic("sqldb: ON CONFLICT needs a column list")
	}
	cl := matchParen(ts, o)
	var keys []string
	for _, k := range splitTop(ts, o+1, cl) {
		keys = append(keys, strings.TrimSpace(join(k)))
	}
	d := sigFwd(ts, cl+1)
	if d >= len(ts) || !ts[d].is("DO") {
		panic("sqldb: ON CONFLICT needs DO")
	}
	a := sigFwd(ts, d+1)
	insert := ts[:oc]
	var assigns []token
	nothing := false
	if ts[a].is("NOTHING") {
		nothing = true
	} else if ts[a].is("UPDATE") {
		s := sigFwd(ts, a+1)
		assigns = trimSpace(ts[sigFwd(ts, s+1):])
	} else {
		panic("sqldb: unsupported ON CONFLICT action")
	}
	switch p {
	case MySQL:
		out := append([]token(nil), trimSpace(insert)...)
		out = append(out, sp(), word("ON"), sp(), word("DUPLICATE"), sp(), word("KEY"), sp(), word("UPDATE"), sp())
		if nothing {
			out = append(out, raw(keys[0]+"="+keys[0]))
		} else {
			for i := 0; i < len(assigns); i++ { // EXCLUDED.x -> VALUES(x)
				if assigns[i].is("EXCLUDED") && i+2 < len(assigns) && assigns[i+1].isP(".") {
					out = append(out, raw("VALUES("+assigns[i+2].text+")"))
					i += 2
					continue
				}
				out = append(out, assigns[i])
			}
		}
		return out
	default: // MSSQL
		ins := trimSpace(insert)
		// INSERT INTO t (cols) VALUES (...)  |  INSERT INTO t (cols) SELECT ...
		it := sigFwd(ins, 0)
		into := sigFwd(ins, it+1) // INTO
		name := sigFwd(ins, into+1)
		colOpen := sigFwd(ins, name+1)
		if !ins[colOpen].isP("(") {
			panic("sqldb: ON CONFLICT insert needs a column list")
		}
		colClose := matchParen(ins, colOpen)
		var cols []string
		for _, c := range splitTop(ins, colOpen+1, colClose) {
			cols = append(cols, strings.TrimSpace(join(c)))
		}
		v := sigFwd(ins, colClose+1)
		if ins[v].is("SELECT") {
			if !nothing {
				panic("sqldb: ON CONFLICT DO UPDATE is not supported for INSERT ... SELECT")
			}
			return ins // the caller guards duplicates with NOT EXISTS
		}
		if !ins[v].is("VALUES") {
			panic("sqldb: unsupported INSERT form for ON CONFLICT")
		}
		vo := sigFwd(ins, v+1)
		vc := matchParen(ins, vo)
		vals := splitTop(ins, vo+1, vc)
		if len(vals) != len(cols) {
			panic("sqldb: ON CONFLICT column/value count mismatch")
		}
		var b strings.Builder
		b.WriteString("MERGE INTO " + join(ins[name:name+1]) + " WITH (HOLDLOCK) AS tgt USING (SELECT ")
		for i, c := range cols {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strings.TrimSpace(join(vals[i])) + " AS " + c)
		}
		b.WriteString(") AS src ON (")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(" AND ")
			}
			b.WriteString("tgt." + k + " = src." + k)
		}
		b.WriteString(") WHEN NOT MATCHED THEN INSERT (" + strings.Join(cols, ", ") + ") VALUES (")
		for i, c := range cols {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("src." + c)
		}
		b.WriteString(")")
		if !nothing {
			b.WriteString(" WHEN MATCHED THEN UPDATE SET " + join(assigns))
		}
		b.WriteString(";")
		return tokenize(b.String())
	}
}

func placeholders(p Provider, ts []token) translated {
	var idx []int
	for i, t := range ts {
		if t.kind != tkParam {
			continue
		}
		n, _ := strconv.Atoi(t.text[1:])
		switch p {
		case MySQL:
			idx = append(idx, n-1)
			ts[i] = token{tkParam, "?"}
		case MSSQL:
			ts[i] = token{tkParam, "@p" + strconv.Itoa(n)}
		}
	}
	return translated{sql: join(ts), argIdx: idx}
}

// expand maps the caller's arguments to the MySQL positional list.
func (t translated) expand(args []any) ([]any, error) {
	if t.argIdx == nil {
		return args, nil
	}
	out := make([]any, len(t.argIdx))
	for i, n := range t.argIdx {
		if n >= len(args) {
			return nil, fmt.Errorf("sqldb: statement uses $%d but only %d arguments were given", n+1, len(args))
		}
		out[i] = args[n]
	}
	return out, nil
}
