//go:build !windows

package server

import (
	"os/exec"
	"syscall"
)

// detach configures cmd so the supervisor survives the launching shell.
// Setsid puts it in a new session, detaching it from the controlling terminal
// so a Ctrl-C in that terminal does not reach the server.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
