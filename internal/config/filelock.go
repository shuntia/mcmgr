package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// lockTimeout bounds how long a command waits for another mcmgr process to
// finish writing. A real write takes milliseconds, so anything approaching
// this means the other process is stuck rather than busy.
const lockTimeout = 5 * time.Second

// errLockTimeout is what the platform lock helpers return when the wait ran
// out; every other error is reported as-is.
var errLockTimeout = errors.New("timed out waiting for the file lock")

// LockPath is the advisory lock file guarding path. It is a sibling rather
// than the file itself because the files mcmgr writes are replaced by a
// rename, which would leave a lock held on a file no longer at that name.
func LockPath(path string) string {
	dir, base := filepath.Split(path)
	return filepath.Join(dir, "."+base+".lock")
}

// WithLock runs fn while holding an exclusive advisory lock on path, so two
// mcmgr invocations cannot interleave a read-modify-write of the same file and
// silently lose one another's changes.
//
// The lock is advisory and process-wide state on the operating system's side:
// it stops other mcmgr processes, not an editor or a script writing the file
// directly. Callers should hold it across the whole load-modify-save cycle,
// not just the write.
func WithLock(path string, fn func() error) error {
	return withLockTimeout(path, lockTimeout, fn)
}

// withLockTimeout is WithLock with the wait exposed, for tests.
func withLockTimeout(path string, timeout time.Duration, fn func() error) error {
	lp := LockPath(path)
	if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("opening lock file %s: %w", lp, err)
	}
	defer f.Close()

	if err := lockExclusive(f, timeout); err != nil {
		if errors.Is(err, errLockTimeout) {
			return fmt.Errorf("gave up after %s waiting to write %s\n"+
				"       another mcmgr command is holding it; let that one finish and try again\n"+
				"       (if no mcmgr is running, the lock file %s can be deleted)",
				timeout, path, lp)
		}
		return fmt.Errorf("locking %s: %w", lp, err)
	}
	defer unlockFile(f)

	return fn()
}

// WriteFileLocked writes data to path atomically while holding its lock.
func WriteFileLocked(path string, data []byte) error {
	return WithLock(path, func() error { return WriteFileAtomic(path, data) })
}

// WriteJSONLocked marshals v to path atomically while holding its lock.
func WriteJSONLocked(path string, v any) error {
	return WithLock(path, func() error { return WriteJSON(path, v) })
}
