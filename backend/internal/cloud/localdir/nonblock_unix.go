//go:build unix

package localdir

import "syscall"

// openExtra makes open(2) of a swapped-in FIFO/device return immediately instead of blocking; the descriptor is
// then rejected by the post-open regular-file check.
const openExtra = syscall.O_NONBLOCK
