//go:build !windows

package imports

import "syscall"

// FreeDiskSpace is the space available to unprivileged writers on dir's volume.
func FreeDiskSpace(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
