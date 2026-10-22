package tui

import (
	"os"
	"path"
	"sort"
	"strings"
)

// completeDir returns the directories that start with the typed prefix (directories only,
// hidden ones only when the prefix asks for them), each with a trailing slash.
func completeDir(prefix string) []string {
	if !strings.HasPrefix(prefix, "/") {
		return nil
	}
	dir, base := path.Split(prefix)
	if dir == "" {
		dir = "/"
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, base) || (strings.HasPrefix(n, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(path.Join(dir, n)); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if isDir {
			out = append(out, path.Join(dir, n)+"/")
		}
	}
	sort.Strings(out)
	return out
}

// commonPrefix is the longest shared prefix of the candidates.
func commonPrefix(c []string) string {
	if len(c) == 0 {
		return ""
	}
	p := c[0]
	for _, s := range c[1:] {
		for !strings.HasPrefix(s, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

// nextCompletion implements tab: extend to the common prefix, then cycle through the candidates.
func nextCompletion(cur string, last []string) (string, []string) {
	if len(last) > 1 {
		for i, c := range last {
			if c == cur {
				return last[(i+1)%len(last)], last
			}
		}
	}
	c := completeDir(cur)
	switch len(c) {
	case 0:
		return cur, nil
	case 1:
		return c[0], c
	}
	if p := commonPrefix(c); len(p) > len(cur) {
		return p, c
	}
	return c[0], c
}
