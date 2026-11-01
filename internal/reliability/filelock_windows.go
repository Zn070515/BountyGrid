//go:build windows

package reliability

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
	errorLockViolation      = syscall.Errno(33)
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// overlapped mirrors the Win32 OVERLAPPED structure. The lock is one byte at
// offset zero; the open handle is the lock identity, so a second handle to the
// same file conflicts. CloseHandle (os.File.Close) releases the lock if the
// process exits, so there is no stale-by-mtime recovery to race.
type overlapped struct {
	Internal     uintptr
	InternalHigh uintptr
	Offset       uint32
	OffsetHigh   uint32
	HEvent       syscall.Handle
}

func tryLockFile(f *os.File) (bool, error) {
	var ol overlapped
	r1, _, err := procLockFileEx.Call(
		f.Fd(),
		lockfileExclusiveLock|lockfileFailImmediately,
		0, 1, 0,
		uintptr(unsafe.Pointer(&ol)),
	)
	if r1 != 0 {
		return true, nil
	}
	if err == errorLockViolation {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) error {
	var ol overlapped
	r1, _, err := procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r1 == 0 {
		return err
	}
	return nil
}
