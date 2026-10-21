package pipeline

import "io"

type ioReader = io.Reader

// copyN copies up to limit+1 bytes so callers can detect oversize input.
func copyN(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	return io.Copy(dst, io.LimitReader(src, limit+1))
}
