//go:build windows

package server

import (
	"os/exec"
	"syscall"
)

// createNoWindow starts a console program without giving it a console window.
const createNoWindow = 0x08000000

// hideConsole stops a child console program from opening a window of its own.
//
// This matters because the supervisor is started with DETACHED_PROCESS and so
// has no console at all. When a process with no console starts a console
// program such as java.exe, Windows allocates a brand new console for it, which
// appears as a stray terminal window. CREATE_NO_WINDOW asks for no console
// instead; the child's stdio still comes from the pipes it was given, so
// nothing is lost.
func hideConsole(cmd *exec.Cmd) {
	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
	}
	attr.CreationFlags |= createNoWindow
	attr.HideWindow = true
	cmd.SysProcAttr = attr
}

// isolateChild additionally puts the server JVM in a process group of its own.
//
// In a foreground run the supervisor shares the user's console, and a Ctrl-C
// there is otherwise delivered to every process attached to that console,
// java.exe included. The server must only ever be stopped through its console
// "stop" command, so the JVM is kept out of the console's Ctrl-C delivery and
// the supervisor decides what a signal means.
func isolateChild(cmd *exec.Cmd) {
	hideConsole(cmd)
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
}
