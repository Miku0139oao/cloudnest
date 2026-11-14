//go:build windows

package platform

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInfoClass = 9
	jobObjectLimitKillOnJobClose    = 0x2000

	processSetQuota  = 0x0100
	processTerminate = 0x0001
)

var (
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
)

// jobObjectBasicLimitInformation mirrors JOBOBJECT_BASIC_LIMIT_INFORMATION.
// The field order and padding match the 64-bit Windows ABI.
type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

// ioCounters mirrors IO_COUNTERS.
type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

// jobObjectExtendedLimitInformation mirrors
// JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// JobObject is a Windows Job Object configured with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE. Assign rclone to it so that closing the
// job handle (which happens when Cloudnest exits) terminates rclone and
// prevents orphan processes.
type JobObject struct {
	handle syscall.Handle
}

// NewJobObject creates a job object which kills all assigned processes when
// its last handle is closed.
func NewJobObject() (*JobObject, error) {
	h, _, callErr := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return nil, fmt.Errorf("platform: CreateJobObject: %w", callErr)
		}
		return nil, fmt.Errorf("platform: CreateJobObject failed")
	}

	var info jobObjectExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	r1, _, callErr := procSetInformationJobObject.Call(
		h,
		uintptr(jobObjectExtendedLimitInfoClass),
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if r1 == 0 {
		_ = syscall.CloseHandle(syscall.Handle(h))
		if callErr != nil && callErr != syscall.Errno(0) {
			return nil, fmt.Errorf("platform: SetInformationJobObject: %w", callErr)
		}
		return nil, fmt.Errorf("platform: SetInformationJobObject failed")
	}
	return &JobObject{handle: syscall.Handle(h)}, nil
}

// Assign places the process identifed by pid into the job. The caller keeps
// ownership of the process handle; this method opens and closes its own.
func (j *JobObject) Assign(pid int) error {
	if j == nil || j.handle == 0 {
		return fmt.Errorf("platform: JobObject.Assign: job is closed")
	}
	if pid <= 0 {
		return fmt.Errorf("platform: JobObject.Assign: invalid pid %d", pid)
	}
	ph, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("platform: OpenProcess(%d): %w", pid, err)
	}
	defer func() { _ = syscall.CloseHandle(ph) }()

	r1, _, callErr := procAssignProcessToJobObject.Call(uintptr(j.handle), uintptr(ph))
	if r1 == 0 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return fmt.Errorf("platform: AssignProcessToJobObject(%d): %w", pid, callErr)
		}
		return fmt.Errorf("platform: AssignProcessToJobObject(%d) failed", pid)
	}
	return nil
}

// Close releases the job handle. Because the job was created with
// KILL_ON_JOB_CLOSE this also terminates any process still assigned to it.
func (j *JobObject) Close() error {
	if j == nil || j.handle == 0 {
		return nil
	}
	err := syscall.CloseHandle(j.handle)
	j.handle = 0
	if err != nil {
		return fmt.Errorf("platform: close job object: %w", err)
	}
	return nil
}
