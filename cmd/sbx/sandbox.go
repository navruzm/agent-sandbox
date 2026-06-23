package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	sandboxDir         = ".sbx"
	claudeConfigVolume = "claude-config"
	imageBase          = "sbx-base"                        // shared image used when a project has no override
	imagePrefix        = "sbx"                             // per-project override images are <prefix>-<name>
	miseDataDir        = "/home/appuser/.local/share/mise" // mise installs runtimes here; cached per project
	projectLabel       = "sbx.project"                     // container label = projectName; lets N instances coexist
)

// CmdSpec describes an external command to run.
type CmdSpec struct {
	Name     string
	Args     []string
	ExtraEnv []string // appended to the environment; keys here override inherited ones
	Silent   bool     // discard stdout/stderr instead of inheriting them
}

// Runner executes external commands. code is the process exit code; notFound is
// true iff the executable could not be found on PATH.
type Runner interface {
	Run(spec CmdSpec) (code int, notFound bool)
	// RunOut runs the command capturing stdout (used for engine queries).
	RunOut(spec CmdSpec) (out string, code int, notFound bool)
}

// engineConf captures how to drive a particular container engine.
type engineConf struct {
	bin       string   // "podman" | "docker"
	runFlags  []string // engine-specific flags inserted into `run`
	volSuffix string   // bind-mount option suffix, e.g. ":U,z"
	microVM   bool     // whether this engine gives a krun microVM (informational)
}

// engineFor builds the engine configuration. podman is the default and the only
// one that gets the krun microVM + keep-id host-UID mapping; docker is a
// best-effort, reduced-isolation fallback (plain container).
func engineFor(name string, uid, gid, cpus, ramMiB int) engineConf {
	switch name {
	case "docker":
		return engineConf{
			bin: "docker",
			runFlags: []string{
				"--user", fmt.Sprintf("%d:%d", uid, gid),
				"--cpus", strconv.Itoa(cpus),
				"--memory", fmt.Sprintf("%dm", ramMiB),
			},
			volSuffix: ":z",
			microVM:   false,
		}
	default: // podman
		return engineConf{
			bin: "podman",
			runFlags: []string{
				"--userns", "keep-id:uid=1000,gid=1000",
				"--annotation", "run.oci.handler=krun",
				"--annotation", fmt.Sprintf("krun.ram_mib=%d", ramMiB),
				"--annotation", fmt.Sprintf("krun.cpus=%d", cpus),
			},
			volSuffix: ":U,z",
			microVM:   true,
		}
	}
}

// envInt reads a positive integer env var via the getenv seam, falling back to def.
func envInt(key string, def int) int {
	if v := getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// App holds the runtime dependencies of a single sbx invocation. Tests
// substitute the writers, the runner, and the engine.
type App struct {
	Cwd       string
	Stdout    io.Writer
	Stderr    io.Writer
	Runner    Runner
	Engine    engineConf
	Verbose   bool
	scopeStop string // ancestor-walk for a scope stops here (e.g. $HOME); empty = filesystem root
}

// Seams so tests can simulate filesystem failures and a non-POSIX host.
var (
	writeFile = os.WriteFile
	chmod     = os.Chmod
	removeAll = os.RemoveAll
	mkdirTemp = os.MkdirTemp
	getuid    = os.Getuid
	getgid    = os.Getgid
	getenv    = os.Getenv
)

// defaultRunCmd is what `sbx run` launches when no command is given: the agent,
// with permission prompts off (safe because the microVM is the isolation boundary).
var defaultRunCmd = []string{"claude", "--dangerously-skip-permissions"}

// envEnabled reports whether an on/off env toggle is set to a truthy value.
func envEnabled(key string) bool {
	switch strings.ToLower(getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

var slugRE = regexp.MustCompile(`[^a-z0-9_.-]+`)

// slugify turns a directory name into a container-safe identifier.
func slugify(name string) string {
	slug := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(name), "-"), "-._")
	if slug == "" {
		return "sandbox"
	}
	return slug
}

// pathHash returns a short, stable hash of an absolute path.
func pathHash(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:])[:8]
}

// projectName is a deterministic per-directory identifier (slug + path hash), so
// the container/volume names are stable across runs without storing any state.
func (a *App) projectName() string {
	return slugify(filepath.Base(a.Cwd)) + "-" + pathHash(a.Cwd)
}

// scopeRoot walks up from the cwd to the nearest directory containing
// .sbx/ — a "sandbox scope" shared by its subdirectories (so a config
// at a parent directory applies to all repos beneath it). Falls back to the cwd.
func (a *App) scopeRoot() (string, bool) {
	dir := a.Cwd
	for {
		if isDir(filepath.Join(dir, sandboxDir)) {
			return dir, true
		}
		if dir == a.scopeStop { // don't search above the boundary (e.g. $HOME)
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached the filesystem root
			break
		}
		dir = parent
	}
	return a.Cwd, false
}

// scopeName is the stable identifier of the scope root (slug + path hash).
func (a *App) scopeName() string {
	root, _ := a.scopeRoot()
	return slugify(filepath.Base(root)) + "-" + pathHash(root)
}

// overrideContext returns the scope's build context if it ships a Dockerfile.
func (a *App) overrideContext() (string, bool) {
	root, found := a.scopeRoot()
	if !found {
		return "", false
	}
	ctx := filepath.Join(root, sandboxDir)
	if fileExists(filepath.Join(ctx, "Dockerfile")) {
		return ctx, true
	}
	return "", false
}

// imageTag is the scope's override image when one is present, else the shared base.
func (a *App) imageTag() string {
	if _, ok := a.overrideContext(); ok {
		return imagePrefix + "-" + a.scopeName()
	}
	return imageBase
}

func (a *App) verboseEcho(argv []string) {
	if a.Verbose {
		fmt.Fprintln(a.Stderr, "+ "+a.Engine.bin+" "+strings.Join(argv, " "))
	}
}

// invoke runs the engine with argv, inheriting stdio, mapping a missing engine to 127.
func (a *App) invoke(argv []string) int {
	a.verboseEcho(argv)
	code, notFound := a.Runner.Run(CmdSpec{Name: a.Engine.bin, Args: argv})
	if notFound {
		fmt.Fprintf(a.Stderr, "`%s` not found on PATH.\n", a.Engine.bin)
		return 127
	}
	return code
}

// resourceExists checks `<engine> <kind> inspect <name>` quietly. notFound means
// the engine binary is missing.
func (a *App) resourceExists(kind, name string) (exists, notFound bool) {
	code, nf := a.Runner.Run(CmdSpec{Name: a.Engine.bin, Args: []string{kind, "inspect", name}, Silent: true})
	return code == 0, nf
}

// cmdBuild builds the image for this project (the per-project override if present,
// otherwise the shared base image from the embedded templates).
func (a *App) cmdBuild() int {
	if ctx, ok := a.overrideContext(); ok {
		return a.invoke([]string{"build", "-t", a.imageTag(), ctx})
	}
	ctx, err := a.baseContext()
	if err != nil {
		fmt.Fprintln(a.Stderr, "build:", err)
		return 1
	}
	defer func() { _ = removeAll(ctx) }()
	return a.invoke([]string{"build", "-t", imageBase, ctx})
}

// baseContext materializes the embedded Dockerfile + entrypoint into a temp build
// context for the shared image.
func (a *App) baseContext() (string, error) {
	dir, err := mkdirTemp("", "sbx-ctx-")
	if err != nil {
		return "", err
	}
	if err := writeFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfileTemplate), 0o644); err != nil {
		_ = removeAll(dir)
		return "", err
	}
	if err := writeFile(filepath.Join(dir, "entrypoint.sh"), []byte(entrypointTemplate), 0o755); err != nil {
		_ = removeAll(dir)
		return "", err
	}
	return dir, nil
}

// cmdRun starts the sandbox, optionally running a command inside it.
func (a *App) cmdRun(cmd []string) int {
	if getuid() < 0 || getgid() < 0 {
		fmt.Fprintln(a.Stderr, "sbx requires a POSIX host with a numeric UID/GID (Linux or macOS).")
		return 1
	}
	tag := a.imageTag()
	exists, notFound := a.resourceExists("image", tag)
	if notFound {
		fmt.Fprintf(a.Stderr, "`%s` not found on PATH.\n", a.Engine.bin)
		return 127
	}
	if !exists {
		fmt.Fprintf(a.Stderr, "Image %s not found; building it...\n", tag)
		if rc := a.cmdBuild(); rc != 0 {
			return rc
		}
	}
	c := stripDashDash(cmd)
	if len(c) == 0 { // no command → run the agent autonomously
		c = append([]string(nil), defaultRunCmd...)
	}
	return a.invoke(a.runArgs(tag, c))
}

// runArgs builds the engine `run` argv: an ephemeral container with the project
// bind-mounted at /app and the persistent claude-config + per-project venv volumes.
func (a *App) runArgs(tag string, cmd []string) []string {
	name := a.projectName()
	v := a.Engine.volSuffix
	// Credential volume: per-cwd if explicitly isolated; else scoped to the nearest
	// ancestor .sbx/ (so subdirectories share the scope's login); else the
	// global shared volume.
	claudeVol := claudeConfigVolume
	switch {
	case envEnabled("SBX_ISOLATED_CONFIG"):
		claudeVol = name + "-claude-config"
	default:
		if _, found := a.scopeRoot(); found {
			claudeVol = a.scopeName() + "-claude-config"
		}
	}

	// Tag with a label (not a fixed --name) so multiple instances can run
	// concurrently for the same project; ps/exec/down find them by this label.
	argv := []string{"run", "--rm", "-it", "--label", projectLabel + "=" + name}
	argv = append(argv, a.Engine.runFlags...)
	argv = append(argv,
		"-v", a.Cwd+":/app"+v,
		"-v", name+"-mise:"+miseDataDir+v,
		"-v", claudeVol+":/home/appuser/.claude"+v,
	)
	// Optionally overlay .git read-only so the agent can't plant a hook that would
	// run on the host at your next commit.
	if envEnabled("SBX_READONLY_GIT") {
		if gitPath := filepath.Join(a.Cwd, ".git"); fileExists(gitPath) {
			argv = append(argv, "-v", gitPath+":/app/.git:ro,z")
		}
	}
	argv = append(argv,
		"-w", "/app",
		"-e", "CLAUDE_CONFIG_DIR=/home/appuser/.claude",
	)
	// Forward host env by reference (value-less `-e NAME`, so secrets never hit the
	// command line). TERM/COLORTERM let the in-container TUI match the real terminal
	// — e.g. tmux-256color, which advertises synchronized output and so doesn't
	// flicker, unlike a hardcoded xterm-256color.
	for _, k := range []string{"TERM", "COLORTERM", "GITHUB_TOKEN", "GOPRIVATE"} {
		if getenv(k) != "" {
			argv = append(argv, "-e", k)
		}
	}
	argv = append(argv, tag)
	return append(argv, cmd...)
}

// projectFilter selects this project's containers by label.
func (a *App) projectFilter() string { return "label=" + projectLabel + "=" + a.projectName() }

// projectContainers returns the IDs of this project's containers (running only,
// unless all). notFound means the engine binary is missing.
func (a *App) projectContainers(all bool) (ids []string, notFound bool) {
	args := []string{"ps"}
	if all {
		args = append(args, "-a")
	}
	args = append(args, "-q", "--filter", a.projectFilter())
	out, _, nf := a.Runner.RunOut(CmdSpec{Name: a.Engine.bin, Args: args, Silent: true})
	if nf {
		return nil, true
	}
	return strings.Fields(out), false
}

// cmdExec runs a command in a running sandbox container (the most recent one if
// several are running).
func (a *App) cmdExec(cmd []string) int {
	ids, notFound := a.projectContainers(false)
	if notFound {
		fmt.Fprintf(a.Stderr, "`%s` not found on PATH.\n", a.Engine.bin)
		return 127
	}
	if len(ids) == 0 {
		fmt.Fprintln(a.Stderr, "No running sandbox; start one with `sbx run`.")
		return 1
	}
	argv := append([]string{"exec", "-it", ids[0]}, stripDashDash(cmd)...)
	return a.invoke(argv)
}

// cmdPs shows this project's sandbox containers (there may be several).
func (a *App) cmdPs() int {
	return a.invoke([]string{"ps", "--filter", a.projectFilter()})
}

// cmdDown stops and removes all of this project's containers.
func (a *App) cmdDown() int {
	ids, notFound := a.projectContainers(true)
	if notFound {
		fmt.Fprintf(a.Stderr, "`%s` not found on PATH.\n", a.Engine.bin)
		return 127
	}
	if len(ids) == 0 {
		fmt.Fprintln(a.Stderr, "No sandbox containers to remove.")
		return 0
	}
	return a.invoke(append([]string{"rm", "-f"}, ids...))
}

// cmdClean removes the container and the per-project mise volume (the shared
// claude-config volume is left intact).
func (a *App) cmdClean() int {
	if rc := a.cmdDown(); rc != 0 {
		return rc // don't remove the volume if the container couldn't be removed
	}
	vol := a.projectName() + "-mise"
	exists, notFound := a.resourceExists("volume", vol)
	if notFound {
		fmt.Fprintf(a.Stderr, "`%s` not found on PATH.\n", a.Engine.bin)
		return 127
	}
	if !exists {
		fmt.Fprintln(a.Stderr, "No tool-cache volume to remove.")
		return 0
	}
	return a.invoke([]string{"volume", "rm", vol})
}

// execRunner is the production Runner: it shells out via os/exec, inheriting
// the standard streams (or discarding them when Silent).
type execRunner struct{}

func (execRunner) Run(spec CmdSpec) (int, bool) {
	cmd := exec.Command(spec.Name, spec.Args...)
	cmd.Env = mergeEnv(os.Environ(), spec.ExtraEnv)
	if !spec.Silent {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	err := cmd.Run()
	if err == nil {
		return 0, false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return 0, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return childExitCode(ee), false
	}
	fmt.Fprintln(os.Stderr, "sbx:", err)
	return 1, false
}

// RunOut captures stdout (stderr discarded); used for engine queries like `ps -q`.
func (execRunner) RunOut(spec CmdSpec) (string, int, bool) {
	cmd := exec.Command(spec.Name, spec.Args...)
	cmd.Env = mergeEnv(os.Environ(), spec.ExtraEnv)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard

	err := cmd.Run()
	if err == nil {
		return out.String(), 0, false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", 0, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), childExitCode(ee), false
	}
	return "", 1, false
}

// mergeEnv appends extra to base, dropping any base entry whose key also appears
// in extra so the injected values win without producing duplicates.
func mergeEnv(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	override := make(map[string]bool, len(extra))
	for _, e := range extra {
		if k, _, ok := strings.Cut(e, "="); ok {
			override[k] = true
		}
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, e := range base {
		if k, _, ok := strings.Cut(e, "="); ok && override[k] {
			continue
		}
		out = append(out, e)
	}
	return append(out, extra...)
}
