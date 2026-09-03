//go:build windows

package ui

import (
	"os"
	"syscall"
	"unsafe"
)

const enableVirtualTerminalProcessing = 0x0004

// enableVT turns on ANSI escape interpretation for the console attached to
// stdout. Windows 10 1511+ supports it but it is off by default for legacy
// console hosts; Windows Terminal already has it on.
func enableVT() bool {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getMode := kernel32.NewProc("GetConsoleMode")
	setMode := kernel32.NewProc("SetConsoleMode")

	h := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	if r, _, _ := getMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return true
	}
	r, _, _ := setMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminalProcessing))
	return r != 0
}
