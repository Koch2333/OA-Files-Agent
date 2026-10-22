//go:build windows

package diag

import (
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// DiskUsage returns free and total bytes of the volume holding path.
func DiskUsage(path string) (free, total uint64, err error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var avail, tot, totFree uint64
	r, _, e := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&tot)), uintptr(unsafe.Pointer(&totFree)))
	if r == 0 {
		return 0, 0, e
	}
	return avail, tot, nil
}
