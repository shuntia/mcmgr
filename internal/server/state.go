// Package server starts, supervises, and talks to Minecraft server processes.
//
// A running server is owned by a supervisor process that holds the JVM's
// stdio and exposes a small line-delimited JSON protocol on a loopback TCP
// port. Every mcmgr invocation is a short-lived client of that protocol, which
// is what makes `attach`, `stop` and the mods-are-locked-while-running check
// work identically on Windows, macOS and Linux.
package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mcmgr/internal/config"
)

// StateName is the runtime state file written inside <server>/.mcmgr.
const StateName = "state.json"

// ConsoleLogName is the captured console output of the current or last run.
const ConsoleLogName = "console.log"

// SupervisorLogName captures the supervisor's own diagnostics, which is where
// startup failures of a detached launch end up.
const SupervisorLogName = "supervisor.log"

// State describes a live supervisor. It exists only while a server is running
// and is removed on clean shutdown.
type State struct {
	// PID of the supervisor process (not the JVM).
	PID int `json:"pid"`
	// ChildPID of the JVM, recorded so an orphaned JVM can still be killed.
	ChildPID int `json:"child_pid"`
	// Port is the loopback control port.
	Port int `json:"port"`
	// Token authenticates control connections.
	Token string `json:"token"`
	// StartedAt is the Unix time the server was launched.
	StartedAt int64 `json:"started_at"`
	// Foreground is true for `server run`, false for `server launch`.
	Foreground bool `json:"foreground"`
	// Name and Dir identify the server for `server list`.
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

// StatePathFor returns the state file path for a server directory.
func StatePathFor(dir string) string {
	return filepath.Join(dir, config.StateDirName, StateName)
}

// ConsoleLogFor returns the console capture path for a server directory.
func ConsoleLogFor(dir string) string {
	return filepath.Join(dir, config.StateDirName, ConsoleLogName)
}

// SupervisorLogFor returns the supervisor diagnostic log path.
func SupervisorLogFor(dir string) string {
	return filepath.Join(dir, config.StateDirName, SupervisorLogName)
}

// ReadState loads the state file for a server directory. A missing file
// returns (nil, nil): that simply means "not running".
func ReadState(dir string) (*State, error) {
	data, err := os.ReadFile(StatePathFor(dir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		// A corrupt state file is treated as stale rather than fatal.
		return nil, nil
	}
	return &s, nil
}

// Write persists the state file with owner-only permissions, since it holds
// the control token.
func (s *State) Write(dir string) error {
	p := StatePathFor(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// ClearState removes the state file.
func ClearState(dir string) error {
	err := os.Remove(StatePathFor(dir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Uptime is how long the server has been up.
func (s *State) Uptime() time.Duration {
	return time.Since(time.Unix(s.StartedAt, 0))
}

// Addr is the loopback control address.
func (s *State) Addr() string { return fmt.Sprintf("127.0.0.1:%d", s.Port) }

// newToken generates a control token.
func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
