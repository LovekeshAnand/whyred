//go:build windows

package main

import (
	"os"
	"syscall"
)

// isTerminal reports whether f is a console that accepts ANSI colour,
// switching on virtual-terminal processing if needed.
func isTerminal(f *os.File) bool {
	h := syscall.Handle(f.Fd())
	var mode uint32
	if syscall.GetConsoleMode(h, &mode) != nil {
		return false
	}
	const enableVT = 0x0004
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode").Call(uintptr(h), uintptr(mode|enableVT))
	return r != 0
}
