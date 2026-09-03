//go:build !windows

package server

import (
	"os/exec"
	"syscall"
)

// hideConsole is a no-op outside Windows, where starting a program never
// creates a window.
func hideConsole(cmd *exec.Cmd) {}

// isolateChild puts the server JVM in a process group of its own.
//
// Without this a Ctrl-C in a foreground run is delivered by the terminal to
// the JVM as well as to the supervisor, and the JVM's own signal handling
// races the "stop" the supervisor is about to send. The server must only
// ever be stopped through its console, so it is kept out of the terminal's
// process group and the supervisor alone decides what a signal means.
func isolateChild(cmd *exec.Cmd) {
	hideConsole(cmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
