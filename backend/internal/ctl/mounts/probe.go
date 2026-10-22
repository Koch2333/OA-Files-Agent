package mounts

import (
	"io/fs"
	"os"
	"sort"
	"strings"
)

// Probe is what the checks need to know about a host path.
type Probe struct {
	Exists     bool
	IsDir      bool
	Mode       fs.FileMode
	UID, GID   int
	FreeBytes  uint64
	TotalBytes uint64
	FSType     string
	Empty      bool
}

// Prober abstracts the filesystem so checks are testable without real mounts.
type Prober interface {
	Probe(path string) (Probe, error)
}

// OSProber probes the real filesystem (statfs and /proc/mounts on Linux; reduced elsewhere).
type OSProber struct{}

func (OSProber) Probe(p string) (Probe, error) {
	st, err := os.Stat(p)
	if os.IsNotExist(err) {
		// free space and fs type of the nearest existing parent still help when the dir will be created
		pr := Probe{}
		for q := parent(p); q != "" && q != p; q = parent(q) {
			if _, e := os.Stat(q); e == nil {
				fillFS(&pr, q)
				break
			}
			if q == "/" {
				break
			}
		}
		return pr, nil
	}
	if err != nil {
		return Probe{}, err
	}
	pr := Probe{Exists: true, IsDir: st.IsDir(), Mode: st.Mode()}
	fillOwner(&pr, st)
	fillFS(&pr, p)
	if pr.IsDir {
		if f, err := os.Open(p); err == nil {
			names, _ := f.Readdirnames(1)
			pr.Empty = len(names) == 0
			f.Close()
		}
	}
	return pr, nil
}

func parent(p string) string {
	i := strings.LastIndex(p, "/")
	switch {
	case i < 0:
		return ""
	case i == 0:
		return "/"
	}
	return p[:i]
}

// MountInfo is one /proc/mounts row.
type MountInfo struct{ Point, FSType string }

// ParseProcMounts parses /proc/mounts content.
func ParseProcMounts(data string) []MountInfo {
	var out []MountInfo
	for _, l := range strings.Split(data, "\n") {
		f := strings.Fields(l)
		if len(f) >= 3 {
			out = append(out, MountInfo{Point: unescapeMount(f[1]), FSType: f[2]})
		}
	}
	return out
}

func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

// FSTypeFor picks the mount with the longest matching mount point.
func FSTypeFor(mounts []MountInfo, p string) string {
	ms := append([]MountInfo(nil), mounts...)
	sort.Slice(ms, func(i, j int) bool { return len(ms[i].Point) > len(ms[j].Point) })
	for _, m := range ms {
		if m.Point == "/" || p == m.Point || strings.HasPrefix(p, strings.TrimSuffix(m.Point, "/")+"/") {
			return m.FSType
		}
	}
	return ""
}
