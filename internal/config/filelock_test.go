package config

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestConcurrentRegistryAddsLoseNothing is the reason the lock exists: two
// mcmgr invocations that each load the registry, add a server and save would
// otherwise leave only the last writer's servers behind.
func TestConcurrentRegistryAddsLoseNothing(t *testing.T) {
	t.Setenv("MCMGR_HOME", t.TempDir())
	dir := t.TempDir()

	const workers, each = 4, 8
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				// Deliberately a stale read: the point is that Add re-reads
				// under the lock rather than trusting this copy.
				reg, err := LoadRegistry()
				if err != nil {
					errs <- err
					return
				}
				if err := reg.Add(fmt.Sprintf("srv-%d-%d", w, i), dir); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("registering concurrently: %v", err)
	}

	final, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(final.Servers); got != workers*each {
		t.Errorf("registry holds %d servers, want %d: concurrent writes lost entries",
			got, workers*each)
	}
	for w := 0; w < workers; w++ {
		for i := 0; i < each; i++ {
			name := fmt.Sprintf("srv-%d-%d", w, i)
			if _, ok := final.Servers[name]; !ok {
				t.Errorf("%s is missing from the registry", name)
			}
		}
	}
	if final.Default == "" {
		t.Error("the first registered server should have become the default")
	}
}

// TestWithLockSerialises checks the lock actually excludes, by making an
// unlocked interleaving visible: without it, both halves would see the flag
// set at once.
func TestWithLockSerialises(t *testing.T) {
	path := t.TempDir() + "/target"

	var mu sync.Mutex
	inside, overlapped := 0, false

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithLock(path, func() error {
				mu.Lock()
				inside++
				if inside > 1 {
					overlapped = true
				}
				mu.Unlock()

				time.Sleep(10 * time.Millisecond)

				mu.Lock()
				inside--
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Errorf("WithLock: %v", err)
			}
		}()
	}
	wg.Wait()

	if overlapped {
		t.Error("two holders were inside the lock at the same time")
	}
}

// TestWithLockTimeoutExplains covers the message a stuck process produces: it
// has to name the file and the lock, since that is all the user can act on.
func TestWithLockTimeoutExplains(t *testing.T) {
	path := t.TempDir() + "/target"

	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = WithLock(path, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer close(release)

	err := withLockTimeout(path, 50*time.Millisecond, func() error {
		t.Error("fn must not run while another holder has the lock")
		return nil
	})
	if err == nil {
		t.Fatal("a contended lock should time out rather than block forever")
	}
	for _, want := range []string{path, LockPath(path), "another mcmgr command"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the timeout error should mention %q, got: %v", want, err)
		}
	}
}
