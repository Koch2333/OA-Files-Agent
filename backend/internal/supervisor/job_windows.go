//go:build windows

package supervisor

import (
	"log"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TieChildrenToProcess puts this process into a Job Object that kills every member when the last handle closes,
// i.e. when this process dies for any reason (crash, taskkill /F, log.Fatal). Children started afterwards — the
// document worker and the PostgreSQL postmaster started through pg_ctl — inherit the job, so a hard kill of the API
// no longer leaves an orphaned database or worker behind. A graceful exit still stops PostgreSQL cleanly first.
func TieChildrenToProcess() {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		log.Printf("job object: %v (helper processes may outlive a crash)", err)
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		log.Printf("job object limits: %v", err)
		_ = windows.CloseHandle(job)
		return
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		log.Printf("job object assign: %v (helper processes may outlive a crash)", err)
		_ = windows.CloseHandle(job)
		return
	}
	// The handle is deliberately never closed: it lives as long as the process.
}
