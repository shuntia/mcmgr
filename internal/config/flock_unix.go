//go:build !windows

package config

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// lockRetry is how often a blocked lock attempt is retried. flock can block
// for us, but not with a deadline, so it is polled non-blocking instead.
const lockRetry = 20 * time.Millisecond

// lockExclusive takes an exclusive flock on f, giving up after timeout.
//
// flock is held per open file description, so two goroutines in one process
// that each opened the lock file contend exactly as two processes would.
func lockExclusive(f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if time.Now().After(deadline) {
			return errLockTimeout
		}
		time.Sleep(lockRetry)
	}
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
