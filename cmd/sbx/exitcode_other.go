//go:build !unix

package main

import "os/exec"

func childExitCode(ee *exec.ExitError) int { return ee.ExitCode() }
