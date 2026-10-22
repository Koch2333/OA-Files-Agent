//go:build !linux

package mounts

import "os"

// oa-agentctl manages Linux servers; other platforms only compile for development and tests.
func fillOwner(pr *Probe, st os.FileInfo) {}
func fillFS(pr *Probe, p string)          {}
