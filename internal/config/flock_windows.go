//go:build windows

package config

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

// LockFileEx is the Windows equivalent of flock. The standard library does not
// export it, so it is resolved from kernel32 rather than pulling in a
// dependency for two calls.
var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
	// errLockViolation is what LockFileEx reports when another handle holds
	// the range and LOCKFILE_FAIL_IMMEDIATELY was asked for.
	errLockViolation = syscall.Errno(33)
)

// lockRetry is how often a blocked lock attempt is retried, so the wait can be
// bounded by a deadline rather than blocking indefinitely in the kernel.
const lockRetry = 20 * time.Millisecond

// lockExclusive takes an exclusive lock on the first byte of f, giving up
// after timeout. Locking one byte rather than the whole file is the usual
// convention: the range need only be the same for every locker.
func lockExclusive(f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var overlapped syscall.Overlapped
		r1, _, err := procLockFileEx.Call(
			f.Fd(),
			lockfileExclusiveLock|lockfileFailImmediately,
			0, 1, 0,
			uintptr(unsafe.Pointer(&overlapped)),
		)
		if r1 != 0 {
			return nil
		}
		if errno, ok := err.(syscall.Errno); !ok || errno != errLockViolation {
			return err
		}
		if time.Now().After(deadline) {
			return errLockTimeout
		}
		time.Sleep(lockRetry)
	}
}

func unlockFile(f *os.File) error {
	var overlapped syscall.Overlapped
	r1, _, err := procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if r1 == 0 {
		return err
	}
	return nil
}
