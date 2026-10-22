//go:build !windows

package diag

import "syscall"

// DiskUsage returns free and total bytes of the file system holding path.
func DiskUsage(path string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err = syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), uint64(st.Blocks) * uint64(st.Bsize), nil
}
