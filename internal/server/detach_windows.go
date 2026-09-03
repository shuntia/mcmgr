//go:build windows

package server

import (
	"os/exec"
	"syscall"
)

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

// detach configures cmd so the supervisor survives the launching console.
// DETACHED_PROCESS gives it no console at all (so no window flashes up and no
// Ctrl-C is delivered), and a new process group isolates it from the parent's.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
		HideWindow:    true,
	}
}
