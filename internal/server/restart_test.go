package server

import (
	"testing"
	"time"

	"mcmgr/internal/config"
)

// newRestartSupervisor builds a supervisor with no JVM behind it, so the
// restart decision can be tested without starting anything.
func newRestartSupervisor(cfg *config.Config) *supervisor {
	return &supervisor{
		cfg:        cfg,
		hub:        newHub(),
		exited:     make(chan struct{}),
		stopWanted: make(chan struct{}),
		st:         &State{},
	}
}

// TestRestartOnlyAfterAnUnexpectedExit is the safety property: a stop mcmgr
// asked for must never be answered by bringing the server back.
func TestRestartOnlyAfterAnUnexpectedExit(t *testing.T) {
	cfg := &config.Config{Name: "test", RestartOnCrash: true}
	cfg.SetDir(t.TempDir())

	// A crash with the setting on is the case that does restart.
	s := newRestartSupervisor(cfg)
	if _, ok := s.shouldRestart(1, time.Minute); !ok {
		t.Error("a non-zero exit with restart_on_crash set should restart")
	}

	// A clean exit is the server doing as it was told.
	s = newRestartSupervisor(cfg)
	if _, ok := s.shouldRestart(0, time.Minute); ok {
		t.Error("exit code 0 should not trigger a restart")
	}

	// So is any exit after a stop was requested, whatever the code.
	s = newRestartSupervisor(cfg)
	s.stopping.Store(true)
	if _, ok := s.shouldRestart(1, time.Minute); ok {
		t.Error("a requested stop must not be treated as a crash")
	}

	// And with the setting off, nothing restarts.
	off := &config.Config{Name: "test"}
	off.SetDir(cfg.Dir())
	s = newRestartSupervisor(off)
	if _, ok := s.shouldRestart(1, time.Minute); ok {
		t.Error("restart_on_crash is off, so a crash should be final")
	}
}

// TestRestartGivesUpOnACrashLoop stops a server that cannot start from being
// restarted forever.
func TestRestartGivesUpOnACrashLoop(t *testing.T) {
	cfg := &config.Config{Name: "test", RestartOnCrash: true, RestartMaxRetries: 2}
	cfg.SetDir(t.TempDir())
	s := newRestartSupervisor(cfg)

	// Two quick crashes are within the allowance.
	for i := 1; i <= 2; i++ {
		if _, ok := s.shouldRestart(1, time.Second); !ok {
			t.Fatalf("restart %d of 2 should have been allowed", i)
		}
	}
	// The third is not: the server is clearly not going to come up.
	if _, ok := s.shouldRestart(1, time.Second); ok {
		t.Error("the retry limit should stop the loop")
	}

	// A run that lasted long enough to look healthy earns the allowance back,
	// so a server that falls over after a week is not permanently out of
	// retries.
	if _, ok := s.shouldRestart(1, healthyRun+time.Minute); !ok {
		t.Error("a long healthy run should reset the retry counter")
	}
	if got := s.restarts.Load(); got != 1 {
		t.Errorf("restarts = %d, want the counter reset and one attempt spent", got)
	}

	// A negative limit means keep going.
	forever := &config.Config{Name: "test", RestartOnCrash: true, RestartMaxRetries: -1}
	forever.SetDir(cfg.Dir())
	unbounded := newRestartSupervisor(forever)
	unbounded.restarts.Store(100)
	if _, ok := unbounded.shouldRestart(1, time.Second); !ok {
		t.Error("restart_max_retries = -1 should never give up")
	}
}

// TestRestartAbandonedOnStop covers a stop arriving while a restart is waiting
// out its backoff: the server must stay down.
func TestRestartAbandonedOnStop(t *testing.T) {
	cfg := &config.Config{Name: "test", RestartOnCrash: true}
	cfg.SetDir(t.TempDir())
	s := newRestartSupervisor(cfg)

	done := make(chan bool, 1)
	go func() { done <- s.restartAfterCrash(1, time.Second) }()

	// The first backoff is seconds long, so this lands well inside it.
	time.Sleep(50 * time.Millisecond)
	close(s.stopWanted)

	select {
	case restarted := <-done:
		if restarted {
			t.Error("a stop during the backoff must cancel the restart")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the restart did not notice the stop request")
	}
}

func TestRestartBackoffIsBounded(t *testing.T) {
	if got := restartBackoff(1); got != 5*time.Second {
		t.Errorf("first backoff = %s, want 5s", got)
	}
	if restartBackoff(2) <= restartBackoff(1) {
		t.Error("the backoff should grow with each consecutive attempt")
	}
	if got := restartBackoff(100); got != 30*time.Second {
		t.Errorf("backoff = %s, want it capped at 30s", got)
	}
}

func TestRestartRetriesDefault(t *testing.T) {
	cfg := &config.Config{}
	if got := cfg.RestartRetries(); got != config.DefaultRestartRetries {
		t.Errorf("RestartRetries() = %d, want the default %d", got, config.DefaultRestartRetries)
	}
	cfg.RestartMaxRetries = 7
	if got := cfg.RestartRetries(); got != 7 {
		t.Errorf("RestartRetries() = %d, want 7", got)
	}
	cfg.RestartMaxRetries = -1
	if got := cfg.RestartRetries(); got >= 0 {
		t.Errorf("RestartRetries() = %d, want a negative value meaning no limit", got)
	}
}
