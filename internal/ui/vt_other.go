//go:build !windows

package ui

// enableVT is a no-op outside Windows; ANSI sequences work by default.
func enableVT() bool { return true }
