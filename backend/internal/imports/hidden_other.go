//go:build !windows

package imports

import "os"

func isHiddenOrSystem(string, os.FileInfo) bool { return false }
