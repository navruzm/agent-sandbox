package main

import (
	"fmt"
	"os"
)

// realApp wires the production dependencies.
func realApp() *App {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sbx:", err)
		os.Exit(1)
	}
	home, _ := os.UserHomeDir() // bound the scope walk at $HOME so a stray ancestor can't capture projects
	return &App{
		Cwd:       cwd,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		Runner:    execRunner{},
		Engine:    engineFor(os.Getenv("SBX_ENGINE"), getuid(), getgid(), envInt("SBX_CPUS", 4), envInt("SBX_RAM_MIB", 4096)),
		scopeStop: home,
	}
}

func main() { os.Exit(dispatch(os.Args[1:], realApp())) }
