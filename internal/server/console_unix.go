//go:build !windows

package server

import "os/exec"

// hideConsole is a no-op outside Windows, where starting a program never
// creates a window.
func hideConsole(cmd *exec.Cmd) {}
