package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// dispatch parses the global flags, selects the subcommand, and runs it,
// returning the process exit code.
func dispatch(args []string, app *App) int {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" { // explicit end of global flags
			i++
			break
		}
		if a == "" || a[0] != '-' { // the subcommand
			break
		}
		switch a {
		case "-v", "--verbose":
			app.Verbose = true
		case "-h", "--help":
			usage(app.Stdout)
			return 0
		case "--version":
			fmt.Fprintln(app.Stdout, version)
			return 0
		default:
			fmt.Fprintf(app.Stderr, "sbx: unknown flag %q\n", a)
			usage(app.Stderr)
			return 2
		}
		i++
	}

	rest := args[i:]
	if len(rest) == 0 {
		fmt.Fprintln(app.Stderr, "sbx: a command is required")
		usage(app.Stderr)
		return 2
	}
	cmd, rest := rest[0], rest[1:]

	usageErr := func(msg string) int {
		fmt.Fprintln(app.Stderr, msg)
		usage(app.Stderr)
		return 2
	}
	noArgs := func() bool { return len(rest) == 0 }

	switch cmd {
	case "init":
		if !noArgs() {
			return usageErr("sbx init: takes no arguments")
		}
		return app.cmdInit()
	case "build":
		if !noArgs() {
			return usageErr("sbx build: takes no arguments")
		}
		return app.cmdBuild()
	case "down":
		if !noArgs() {
			return usageErr("sbx down: takes no arguments")
		}
		return app.cmdDown()
	case "ps":
		if !noArgs() {
			return usageErr("sbx ps: takes no arguments")
		}
		return app.cmdPs()
	case "clean":
		if !noArgs() {
			return usageErr("sbx clean: takes no arguments")
		}
		return app.cmdClean()
	case "run":
		return app.cmdRun(rest)
	case "exec":
		if len(stripDashDash(rest)) == 0 {
			return usageErr("sbx exec: a command is required")
		}
		return app.cmdExec(rest)
	default:
		return usageErr(fmt.Sprintf("sbx: unknown command %q", cmd))
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `sbx — manage a containerized sandbox for coding agents

USAGE:
  sbx [-v|--verbose] <command> [args]
  sbx --version | -h | --help

COMMANDS:
  run [-- cmd]   start the sandbox; with no command, runs 'claude --dangerously-skip-permissions'
  exec [-- cmd]  run a command in the already-running sandbox
  build          build the image (shared by default, or this project's override)
  init           scaffold a per-project image override (.sbx/Dockerfile)
  ps             show this project's sandbox container status
  down           stop and remove this project's sandbox container
  clean          remove the container and this project's mise volume

ENV:
  SBX_ENGINE=docker      use docker instead of podman (plain container, no microVM)
  SBX_ISOLATED_CONFIG=1  per-project Claude credentials instead of the shared volume
  SBX_READONLY_GIT=1     mount .git read-only (blocks agent-planted git hooks)
  SBX_CPUS, SBX_RAM_MIB  sandbox CPU / RAM limits (default 4 / 4096)
  SBX_FORWARD="A B"      extra host env var names to forward into the sandbox
  GITHUB_TOKEN, GOPRIVATE  forwarded into the container for private git/Go access

By default sbx uses one shared image and needs no per-project setup; run sbx init
only to customize the image for a project. 'sbx run' with no command launches the
agent autonomously; use 'sbx run -- bash' for a shell. A leading '--' separates sbx
from the command run inside the sandbox.
`)
}

// stripDashDash drops a single leading "--", so `sbx run -- npm test` and
// `sbx run npm test` are equivalent.
func stripDashDash(cmd []string) []string {
	if len(cmd) > 0 && cmd[0] == "--" {
		return cmd[1:]
	}
	return cmd
}

// cmdInit scaffolds a per-project image override (.sbx/Dockerfile +
// entrypoint.sh) for the user to customize. It is optional — without it, sbx
// uses the shared image.
func (a *App) cmdInit() int {
	target := filepath.Join(a.Cwd, sandboxDir)
	if _, err := os.Stat(target); err == nil {
		fmt.Fprintln(a.Stderr, sandboxDir+"/ already exists; leaving it untouched.")
		return 1
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		fmt.Fprintln(a.Stderr, "init:", err)
		return 1
	}

	files := []struct {
		name string
		data string
		mode os.FileMode
	}{
		{"Dockerfile", dockerfileTemplate, 0o644},
		{"entrypoint.sh", entrypointTemplate, 0o755},
	}
	// fail removes the partial scaffold and reports the original error (plus any
	// cleanup failure), keeping init all-or-nothing.
	fail := func(err error) int {
		if rmErr := removeAll(target); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("cleanup: %w", rmErr))
		}
		fmt.Fprintln(a.Stderr, "init:", err)
		return 1
	}
	for _, f := range files {
		p := filepath.Join(target, f.name)
		if err := writeFile(p, []byte(f.data), f.mode); err != nil {
			return fail(err)
		}
		if err := chmod(p, f.mode); err != nil {
			return fail(err)
		}
	}

	fmt.Fprintf(a.Stdout, "Initialized %s/ (per-project image override)\n", sandboxDir)
	for _, f := range files {
		fmt.Fprintf(a.Stdout, "  - %s/%s\n", sandboxDir, f.name)
	}
	return 0
}
