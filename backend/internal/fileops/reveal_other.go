//go:build !windows

package fileops

import "errors"

func revealSupported() bool { return false }

func launchExplorer(string, bool) error { return errors.New("not supported on this platform") }
