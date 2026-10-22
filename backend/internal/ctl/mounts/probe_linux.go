//go:build linux

package mounts

import (
	"os"
	"syscall"
)

func fillOwner(pr *Probe, st os.FileInfo) {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		pr.UID, pr.GID = int(s.Uid), int(s.Gid)
	}
}

func fillFS(pr *Probe, p string) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(p, &s); err == nil {
		pr.FreeBytes = uint64(s.Bavail) * uint64(s.Bsize)
		pr.TotalBytes = uint64(s.Blocks) * uint64(s.Bsize)
	}
	if b, err := os.ReadFile("/proc/mounts"); err == nil {
		pr.FSType = FSTypeFor(ParseProcMounts(string(b)), p)
	}
}
