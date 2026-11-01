//go:build unix

package reliability

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile attempts a non-blocking exclusive advisory lock. The lock is
// released by the kernel when the file is closed or the process dies, so a
// crash cannot leave a lock that another writer must break by age.
func tryLockFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
