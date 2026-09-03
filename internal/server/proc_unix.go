//go:build !windows

package server

import "syscall"

// processAlive reports whether a pid refers to a live process. Signal 0
// performs the permission and existence checks without delivering anything;
// EPERM means the process exists but is owned by someone else.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
