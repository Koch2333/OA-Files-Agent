//go:build !windows

package supervisor

// TieChildrenToProcess is a no-op outside Windows (containers and systemd stop the whole process group).
func TieChildrenToProcess() {}
