package imports

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Limits are the operator-visible knobs of the import intake. There is NO per-file, per-batch or per-request size cap
// by default: MaxUploadBytes and MaxBatchFiles are optional operator safety valves (0 = unlimited). The one protection
// that is always on is the free-disk-space check (DiskReserve is kept free on the spool volume).
type Limits struct {
	MaxUploadBytes  int64         // OA_MAX_UPLOAD_BYTES: optional per-file cap, 0 = unlimited
	MaxBatchFiles   int           // OA_MAX_BATCH_FILES: optional files-per-batch cap, 0 = unlimited
	DiskReserve     int64         // OA_DISK_RESERVE_BYTES: free space that must stay available (default 512 MiB)
	SessionTTL      time.Duration // OA_UPLOAD_SESSION_TTL_HOURS: abandoned chunked-upload sessions expire (default 24 h)
	MaxSessionsUser int           // abuse protection only: concurrently open upload sessions per user (default 5000)
	FreeSpace       func(dir string) (uint64, error)
	Now             func() time.Time
}

// DefaultDiskReserve is kept free on the spool volume unless OA_DISK_RESERVE_BYTES says otherwise.
const DefaultDiskReserve int64 = 512 << 20

// DefaultChunkBytes is the chunk size advertised to clients; the server accepts any chunk size.
const DefaultChunkBytes int64 = 8 << 20

// LimitsFromEnv reads the OA_* settings. Invalid values fall back to the default (unlimited / built-in default).
func LimitsFromEnv(get func(string) string) Limits {
	if get == nil {
		get = os.Getenv
	}
	num := func(k string) int64 {
		v, err := strconv.ParseInt(strings.TrimSpace(get(k)), 10, 64)
		if err != nil || v < 0 {
			return -1
		}
		return v
	}
	l := Limits{DiskReserve: DefaultDiskReserve, SessionTTL: 24 * time.Hour, MaxSessionsUser: 5000}
	if v := num("OA_MAX_UPLOAD_BYTES"); v > 0 {
		l.MaxUploadBytes = v
	}
	if v := num("OA_MAX_BATCH_FILES"); v > 0 {
		l.MaxBatchFiles = int(v)
	}
	if v := num("OA_DISK_RESERVE_BYTES"); v >= 0 {
		l.DiskReserve = v
	}
	if v := num("OA_UPLOAD_SESSION_TTL_HOURS"); v > 0 {
		l.SessionTTL = time.Duration(v) * time.Hour
	}
	return l
}

func (l Limits) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l Limits) ttl() time.Duration {
	if l.SessionTTL <= 0 {
		return 24 * time.Hour
	}
	return l.SessionTTL
}

func (l Limits) maxSessions() int {
	if l.MaxSessionsUser <= 0 {
		return 5000
	}
	return l.MaxSessionsUser
}

// TooManyFiles reports whether n files exceed the optional batch cap.
func (l Limits) TooManyFiles(n int) bool { return l.MaxBatchFiles > 0 && n > l.MaxBatchFiles }

// TooLarge reports whether a file exceeds the optional per-file cap.
func (l Limits) TooLarge(size int64) bool { return l.MaxUploadBytes > 0 && size > l.MaxUploadBytes }

func (l Limits) free(dir string) (uint64, error) {
	if l.FreeSpace != nil {
		return l.FreeSpace(dir)
	}
	return FreeDiskSpace(dir)
}

// CheckSpace refuses (507 INSUFFICIENT_STORAGE) when `need` more bytes plus the reserve would not fit on dir's volume.
func (l Limits) CheckSpace(dir string, need int64) error {
	if need < 0 {
		need = 0
	}
	free, err := l.free(dir)
	if err != nil {
		return nil // unknown free space must not block imports; ENOSPC is still mapped when a write fails
	}
	if uint64(need)+uint64(l.DiskReserve) > free {
		return NoSpaceError(need, int64(free))
	}
	return nil
}

// NoSpaceError is the 507 error: "not enough disk space: need X, free Y".
func NoSpaceError(need, free int64) *Error {
	return &Error{507, "INSUFFICIENT_STORAGE", fmt.Sprintf("磁盘空间不足：需要 %s，剩余 %s（not enough disk space: need %s, free %s）", HumanBytes(need), HumanBytes(free), HumanBytes(need), HumanBytes(free))}
}

// IsNoSpace recognises "disk full" write errors on every platform.
func IsNoSpace(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ENOSPC) {
		return true
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "no space left") || strings.Contains(m, "not enough space on the disk") || strings.Contains(m, "disk full") || strings.Contains(m, "there is not enough space")
}

// HumanBytes formats a byte count with binary units.
func HumanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	f, unit := float64(n), 0
	for f >= u && unit < 5 {
		f /= u
		unit++
	}
	return fmt.Sprintf("%.1f %s", f, []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}[unit])
}

// Free is the free space of dir's volume (honours the injectable FreeSpace).
func (l Limits) Free(dir string) (uint64, error) { return l.free(dir) }
