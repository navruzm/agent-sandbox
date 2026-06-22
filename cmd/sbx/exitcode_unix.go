//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// childExitCode maps a process failure to a shell-style exit code: 128+signo for
// a signal death (e.g. Ctrl-C => 130), otherwise the process exit status.
func childExitCode(ee *exec.ExitError) int {
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ee.ExitCode()
}
