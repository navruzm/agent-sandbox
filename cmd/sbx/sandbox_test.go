package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRunner records the commands it is asked to run and returns programmed
// results per call index.
type runResult struct {
	code     int
	notFound bool
	out      string
}

type fakeRunner struct {
	calls   []CmdSpec
	results []runResult
}

func (f *fakeRunner) Run(spec CmdSpec) (int, bool) {
	f.calls = append(f.calls, spec)
	i := len(f.calls) - 1
	if i < len(f.results) {
		return f.results[i].code, f.results[i].notFound
	}
	return 0, false
}

func (f *fakeRunner) RunOut(spec CmdSpec) (string, int, bool) {
	f.calls = append(f.calls, spec)
	i := len(f.calls) - 1
	if i < len(f.results) {
		return f.results[i].out, f.results[i].code, f.results[i].notFound
	}
	return "", 0, false
}

func newAppAt(cwd string) (*App, *bytes.Buffer, *bytes.Buffer, *fakeRunner) {
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	fr := &fakeRunner{}
	app := &App{
		Cwd:       cwd,
		Stdout:    out,
		Stderr:    errb,
		Runner:    fr,
		Engine:    engineFor("podman", 1000, 1000, 4, 4096),
		scopeStop: cwd, // by default, don't walk above the test's own dir
	}
	return app, out, errb, fr
}

func newApp(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer, *fakeRunner) {
	t.Helper()
	return newAppAt(t.TempDir())
}

func lastCall(fr *fakeRunner) CmdSpec { return fr.calls[len(fr.calls)-1] }

func TestEnvInt(t *testing.T) {
	orig := getenv
	defer func() { getenv = orig }()
	cases := []struct {
		val  string
		want int
	}{
		{"", 4}, {"8", 8}, {"16384", 16384}, {"notanumber", 4}, {"0", 4}, {"-2", 4},
	}
	for _, c := range cases {
		getenv = func(string) string { return c.val }
		if got := envInt("SBX_X", 4); got != c.want {
			t.Errorf("envInt(%q) = %d, want %d", c.val, got, c.want)
		}
	}
}

func TestEngineResourceLimits(t *testing.T) {
	p := engineFor("podman", 1000, 1000, 8, 16384)
	if !slices.Contains(p.runFlags, "krun.cpus=8") || !slices.Contains(p.runFlags, "krun.ram_mib=16384") {
		t.Errorf("podman resource flags wrong: %v", p.runFlags)
	}
	d := engineFor("docker", 1000, 1000, 8, 16384)
	if i := slices.Index(d.runFlags, "--cpus"); i < 0 || d.runFlags[i+1] != "8" {
		t.Errorf("docker --cpus wrong: %v", d.runFlags)
	}
	if i := slices.Index(d.runFlags, "--memory"); i < 0 || d.runFlags[i+1] != "16384m" {
		t.Errorf("docker --memory wrong: %v", d.runFlags)
	}
}

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"MyProject", "myproject"},
		{"foo bar", "foo-bar"},
		{"...weird@@name..", "weird-name"},
		{"", "sandbox"},
		{"---", "sandbox"},
		{"a.b_c-d", "a.b_c-d"},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPathHashAndProjectName(t *testing.T) {
	h := pathHash("/home/me/proj")
	if len(h) != 8 {
		t.Errorf("pathHash len = %d, want 8 (%q)", len(h), h)
	}
	for _, c := range h {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("pathHash has non-hex char %q in %q", c, h)
		}
	}
	if pathHash("/a") == pathHash("/b") {
		t.Error("pathHash should differ for different paths")
	}

	app, _, _, _ := newAppAt("/home/me/MyProj")
	if want := "myproj-" + pathHash("/home/me/MyProj"); app.projectName() != want {
		t.Errorf("projectName = %q, want %q", app.projectName(), want)
	}
}

func TestScopeRootWalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, sandboxDir), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	app, _, _, _ := newAppAt(sub)
	app.scopeStop = root
	if got, found := app.scopeRoot(); !found || got != root {
		t.Errorf("scopeRoot = (%q, %v), want (%q, true)", got, found, root)
	}

	bare, _, _, _ := newAppAt(t.TempDir())
	if _, found := bare.scopeRoot(); found {
		t.Error("scopeRoot should find nothing in a bare temp dir")
	}
}

func TestImageTagUsesAncestorScope(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, sandboxDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, sandboxDir, "Dockerfile"), []byte("FROM x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	app, _, _, _ := newAppAt(sub)
	app.scopeStop = root
	if want := imagePrefix + "-" + app.scopeName(); app.imageTag() != want {
		t.Errorf("imageTag = %q, want %q (ancestor scope)", app.imageTag(), want)
	}
}

func TestRunArgsAncestorScopedCreds(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, sandboxDir), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	app, _, _, _ := newAppAt(sub)
	app.scopeStop = root
	orig := getenv
	getenv = func(string) string { return "" }
	defer func() { getenv = orig }()

	got := app.runArgs("img", nil)
	v := app.Engine.volSuffix
	if want := app.scopeName() + "-claude-config:/home/appuser/.claude" + v; !slices.Contains(got, want) {
		t.Errorf("want ancestor-scoped creds %q in %v", want, got)
	}
	if global := "claude-config:/home/appuser/.claude" + v; slices.Contains(got, global) {
		t.Error("should not use the global claude-config when an ancestor scope exists")
	}
}

func TestImageTag(t *testing.T) {
	app, _, _, _ := newApp(t)
	if app.imageTag() != imageBase {
		t.Errorf("no override: imageTag = %q, want %q", app.imageTag(), imageBase)
	}

	// add a per-project Dockerfile override
	if err := os.MkdirAll(filepath.Join(app.Cwd, sandboxDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.Cwd, sandboxDir, "Dockerfile"), []byte("FROM x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if want := imagePrefix + "-" + app.projectName(); app.imageTag() != want {
		t.Errorf("override: imageTag = %q, want %q", app.imageTag(), want)
	}
}

func TestRunArgsPodman(t *testing.T) {
	app, _, _, _ := newAppAt("/work/proj")
	orig := getenv
	getenv = func(string) string { return "" } // no GITHUB_TOKEN in this case
	defer func() { getenv = orig }()
	name := app.projectName()
	got := app.runArgs("img:tag", []string{"claude", "--resume"})
	want := []string{
		"run", "--rm", "-it", "--label", "sbx.project=" + name,
		"--userns", "keep-id:uid=1000,gid=1000",
		"--annotation", "run.oci.handler=krun",
		"--annotation", "krun.ram_mib=4096",
		"--annotation", "krun.cpus=4",
		"-v", "/work/proj:/app:U,z",
		"-v", name + "-mise:" + miseDataDir + ":U,z",
		"-v", "claude-config:/home/appuser/.claude:U,z",
		"-w", "/app",
		"-e", "CLAUDE_CONFIG_DIR=/home/appuser/.claude",
		"img:tag", "claude", "--resume",
	}
	if !slices.Equal(got, want) {
		t.Errorf("podman runArgs mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestRunArgsDocker(t *testing.T) {
	app, _, _, _ := newAppAt("/work/proj")
	app.Engine = engineFor("docker", 1000, 1000, 4, 4096)
	orig := getenv
	getenv = func(string) string { return "" }
	defer func() { getenv = orig }()
	got := app.runArgs("img:tag", nil)

	if !slices.Contains(got, "--user") || !slices.Contains(got, "1000:1000") {
		t.Errorf("docker runArgs missing --user 1000:1000: %v", got)
	}
	if !slices.Contains(got, "--cpus") || !slices.Contains(got, "--memory") {
		t.Errorf("docker runArgs missing resource limits: %v", got)
	}
	if slices.Contains(got, "run.oci.handler=krun") {
		t.Errorf("docker runArgs should not carry krun annotation: %v", got)
	}
	if !slices.Contains(got, "/work/proj:/app:z") {
		t.Errorf("docker runArgs should mount with :z (no :U): %v", got)
	}
	if slices.Contains(got, "/work/proj:/app:U,z") {
		t.Errorf("docker runArgs should not use podman :U: %v", got)
	}
}

func TestRunArgsForwardsGithubToken(t *testing.T) {
	app, _, _, _ := newAppAt("/work/proj")
	orig := getenv
	getenv = func(k string) string {
		switch k {
		case "GITHUB_TOKEN":
			return "ghp_secret123"
		case "GOPRIVATE":
			return "github.com/acme/*"
		case "TERM":
			return "tmux-256color"
		case "COLORTERM":
			return "truecolor"
		}
		return ""
	}
	defer func() { getenv = orig }()

	got := app.runArgs("img:tag", nil)

	// Each forwarded as value-less `-e NAME`, so the engine inherits the value
	// from sbx's env and nothing sensitive appears on the command line.
	for _, name := range []string{"TERM", "COLORTERM", "GITHUB_TOKEN", "GOPRIVATE"} {
		idx := slices.Index(got, name)
		if idx < 1 || got[idx-1] != "-e" {
			t.Fatalf("expected `-e %s` passthrough, got %v", name, got)
		}
	}
	for _, a := range got {
		if strings.Contains(a, "ghp_secret123") {
			t.Errorf("token value leaked into argv: %q", a)
		}
	}
}

func TestRunArgsOmitsGithubTokenWhenUnset(t *testing.T) {
	app, _, _, _ := newAppAt("/work/proj")
	orig := getenv
	getenv = func(string) string { return "" }
	defer func() { getenv = orig }()
	if slices.Contains(app.runArgs("img:tag", nil), "GITHUB_TOKEN") {
		t.Error("GITHUB_TOKEN should not be passed when unset on the host")
	}
}

func TestCmdRunDefaultsToClaudeSkipPermissions(t *testing.T) {
	app, _, _, fr := newApp(t)
	fr.results = []runResult{{code: 0}} // image exists
	if rc := app.cmdRun(nil); rc != 0 {
		t.Fatalf("cmdRun = %d, want 0", rc)
	}
	run := fr.calls[len(fr.calls)-1].Args
	tail := run[len(run)-2:]
	if !slices.Equal(tail, []string{"claude", "--dangerously-skip-permissions"}) {
		t.Errorf("default command = %v, want [claude --dangerously-skip-permissions]", tail)
	}
}

func TestCmdRunExplicitCommandNotOverridden(t *testing.T) {
	app, _, _, fr := newApp(t)
	fr.results = []runResult{{code: 0}}
	app.cmdRun([]string{"--", "bash"})
	run := fr.calls[len(fr.calls)-1].Args
	if run[len(run)-1] != "bash" || slices.Contains(run, "--dangerously-skip-permissions") {
		t.Errorf("explicit command should be verbatim, got %v", run)
	}
}

func TestRunArgsIsolatedConfig(t *testing.T) {
	app, _, _, _ := newAppAt("/work/proj")
	orig := getenv
	getenv = func(k string) string {
		if k == "SBX_ISOLATED_CONFIG" {
			return "1"
		}
		return ""
	}
	defer func() { getenv = orig }()

	got := app.runArgs("img", nil)
	v := app.Engine.volSuffix
	if want := app.projectName() + "-claude-config:/home/appuser/.claude" + v; !slices.Contains(got, want) {
		t.Errorf("want per-project config mount %q in %v", want, got)
	}
	if shared := "claude-config:/home/appuser/.claude" + v; slices.Contains(got, shared) {
		t.Error("should not use the shared claude-config volume when isolated")
	}
}

func TestRunArgsReadonlyGit(t *testing.T) {
	app, _, _, _ := newApp(t) // real temp cwd
	if err := os.Mkdir(filepath.Join(app.Cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := getenv
	getenv = func(k string) string {
		if k == "SBX_READONLY_GIT" {
			return "1"
		}
		return ""
	}
	defer func() { getenv = orig }()

	got := app.runArgs("img", nil)
	if want := filepath.Join(app.Cwd, ".git") + ":/app/.git:ro,z"; !slices.Contains(got, want) {
		t.Errorf("want read-only .git mount %q in %v", want, got)
	}
}

func TestRunArgsReadonlyGitSkippedWhenAbsent(t *testing.T) {
	app, _, _, _ := newApp(t) // no .git
	orig := getenv
	getenv = func(k string) string {
		if k == "SBX_READONLY_GIT" {
			return "1"
		}
		return ""
	}
	defer func() { getenv = orig }()

	for _, a := range app.runArgs("img", nil) {
		if strings.HasSuffix(a, "/.git:ro,z") {
			t.Errorf("should not mount .git when it is absent: %v", a)
		}
	}
}

func TestCmdRunUsesExistingImage(t *testing.T) {
	app, _, _, fr := newApp(t)
	fr.results = []runResult{{code: 0}} // image inspect: exists

	if rc := app.cmdRun([]string{"--", "claude"}); rc != 0 {
		t.Fatalf("cmdRun = %d, want 0", rc)
	}
	if len(fr.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (image inspect + run)", len(fr.calls))
	}
	if fr.calls[0].Args[0] != "image" || fr.calls[0].Args[1] != "inspect" {
		t.Errorf("first call = %v, want image inspect", fr.calls[0].Args)
	}
	run := fr.calls[1].Args
	if !slices.Equal(run, app.runArgs(imageBase, []string{"claude"})) {
		t.Errorf("run args = %v", run)
	}
}

func TestCmdRunBuildsMissingImage(t *testing.T) {
	app, _, _, fr := newApp(t)
	fr.results = []runResult{{code: 1}, {code: 0}, {code: 0}} // inspect: missing, build ok, run ok

	if rc := app.cmdRun(nil); rc != 0 {
		t.Fatalf("cmdRun = %d, want 0", rc)
	}
	if len(fr.calls) != 3 {
		t.Fatalf("calls = %d, want 3 (inspect + build + run)", len(fr.calls))
	}
	if fr.calls[1].Args[0] != "build" {
		t.Errorf("second call = %v, want build", fr.calls[1].Args)
	}
	if fr.calls[2].Args[0] != "run" {
		t.Errorf("third call = %v, want run", fr.calls[2].Args)
	}
}

func TestCmdRunEngineNotFound(t *testing.T) {
	app, _, errb, fr := newApp(t)
	fr.results = []runResult{{notFound: true}}
	if rc := app.cmdRun(nil); rc != 127 {
		t.Fatalf("cmdRun = %d, want 127", rc)
	}
	if !strings.Contains(errb.String(), "not found on PATH") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestCmdRunPosixGuard(t *testing.T) {
	app, _, errb, fr := newApp(t)
	orig := getuid
	getuid = func() int { return -1 }
	defer func() { getuid = orig }()

	if rc := app.cmdRun(nil); rc != 1 {
		t.Fatalf("cmdRun = %d, want 1", rc)
	}
	if len(fr.calls) != 0 {
		t.Errorf("runner called %d times, want 0", len(fr.calls))
	}
	if !strings.Contains(errb.String(), "POSIX") {
		t.Errorf("stderr = %q, want POSIX guard", errb.String())
	}
}

func TestCmdExec(t *testing.T) {
	app, _, _, fr := newApp(t)
	fr.results = []runResult{{out: "cafe123\n"}} // ps -q (running containers)
	app.cmdExec([]string{"--", "fish"})
	if want := []string{"exec", "-it", "cafe123", "fish"}; !slices.Equal(lastCall(fr).Args, want) {
		t.Errorf("exec args = %v, want %v", lastCall(fr).Args, want)
	}
}

func TestCmdExecNoRunningContainer(t *testing.T) {
	app, _, errb, fr := newApp(t)
	fr.results = []runResult{{out: ""}} // no running containers
	if rc := app.cmdExec([]string{"--", "fish"}); rc != 1 {
		t.Fatalf("cmdExec = %d, want 1", rc)
	}
	if len(fr.calls) != 1 || !strings.Contains(errb.String(), "No running sandbox") {
		t.Errorf("calls=%d stderr=%q", len(fr.calls), errb.String())
	}
}

func TestCmdPs(t *testing.T) {
	app, _, _, fr := newApp(t)
	app.cmdPs()
	want := []string{"ps", "--filter", "label=" + projectLabel + "=" + app.projectName()}
	if !slices.Equal(lastCall(fr).Args, want) {
		t.Errorf("ps args = %v, want %v", lastCall(fr).Args, want)
	}
}

func TestCmdDownRemovesAllInstances(t *testing.T) {
	app, _, _, fr := newApp(t)
	fr.results = []runResult{{out: "id1 id2"}, {code: 0}} // ps -aq -> two ids, then rm
	if rc := app.cmdDown(); rc != 0 {
		t.Fatalf("cmdDown = %d, want 0", rc)
	}
	if len(fr.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (ps + rm)", len(fr.calls))
	}
	if want := []string{"rm", "-f", "id1", "id2"}; !slices.Equal(fr.calls[1].Args, want) {
		t.Errorf("rm args = %v, want %v", fr.calls[1].Args, want)
	}
}

func TestCmdDownNoContainer(t *testing.T) {
	app, _, errb, fr := newApp(t)
	fr.results = []runResult{{out: ""}} // ps -aq: none
	if rc := app.cmdDown(); rc != 0 {
		t.Fatalf("cmdDown = %d, want 0", rc)
	}
	if len(fr.calls) != 1 {
		t.Errorf("calls = %d, want 1 (ps only)", len(fr.calls))
	}
	if !strings.Contains(errb.String(), "No sandbox containers") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestCmdClean(t *testing.T) {
	app, _, _, fr := newApp(t)
	// ps -aq -> id, rm ok, volume inspect: exists, volume rm ok
	fr.results = []runResult{{out: "id1"}, {code: 0}, {code: 0}, {code: 0}}
	if rc := app.cmdClean(); rc != 0 {
		t.Fatalf("cmdClean = %d, want 0", rc)
	}
	if len(fr.calls) != 4 {
		t.Fatalf("calls = %d, want 4", len(fr.calls))
	}
	if want := []string{"volume", "rm", app.projectName() + "-mise"}; !slices.Equal(lastCall(fr).Args, want) {
		t.Errorf("volume rm args = %v, want %v", lastCall(fr).Args, want)
	}
}

func TestCmdBuildOverride(t *testing.T) {
	app, _, _, fr := newApp(t)
	if err := os.MkdirAll(filepath.Join(app.Cwd, sandboxDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.Cwd, sandboxDir, "Dockerfile"), []byte("FROM x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rc := app.cmdBuild(); rc != 0 {
		t.Fatalf("cmdBuild = %d, want 0", rc)
	}
	want := []string{"build", "-t", imagePrefix + "-" + app.projectName(), filepath.Join(app.Cwd, sandboxDir)}
	if !slices.Equal(lastCall(fr).Args, want) {
		t.Errorf("build args = %v, want %v", lastCall(fr).Args, want)
	}
}

func TestCmdBuildBaseMaterializesContext(t *testing.T) {
	app, _, _, fr := newApp(t)
	ctxDir := filepath.Join(t.TempDir(), "ctx")
	if err := os.Mkdir(ctxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	origMk, origRm := mkdirTemp, removeAll
	mkdirTemp = func(string, string) (string, error) { return ctxDir, nil }
	removeAll = func(string) error { return nil } // keep the context for inspection
	defer func() { mkdirTemp, removeAll = origMk, origRm }()

	if rc := app.cmdBuild(); rc != 0 {
		t.Fatalf("cmdBuild = %d, want 0", rc)
	}
	want := []string{"build", "-t", imageBase, ctxDir}
	if !slices.Equal(lastCall(fr).Args, want) {
		t.Errorf("build args = %v, want %v", lastCall(fr).Args, want)
	}
	for _, f := range []string{"Dockerfile", "entrypoint.sh"} {
		if _, err := os.Stat(filepath.Join(ctxDir, f)); err != nil {
			t.Errorf("context missing %s: %v", f, err)
		}
	}
}

func TestVerboseEcho(t *testing.T) {
	app, _, errb, _ := newApp(t)
	app.Verbose = true
	app.cmdPs()
	if want := "+ podman ps --filter label=" + projectLabel + "=" + app.projectName() + "\n"; errb.String() != want {
		t.Errorf("stderr = %q, want %q", errb.String(), want)
	}
}

func TestInitScaffold(t *testing.T) {
	app, out, _, _ := newApp(t)
	if rc := app.cmdInit(); rc != 0 {
		t.Fatalf("cmdInit = %d, want 0", rc)
	}

	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(app.Cwd, sandboxDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}
	if got := read("Dockerfile"); got != dockerfileTemplate {
		t.Error("Dockerfile does not match the embedded template")
	}
	if got := read("entrypoint.sh"); got != entrypointTemplate {
		t.Error("entrypoint.sh does not match the embedded template")
	}
	// no compose file anymore
	if _, err := os.Stat(filepath.Join(app.Cwd, sandboxDir, "docker-compose.yaml")); !os.IsNotExist(err) {
		t.Error("init should not write docker-compose.yaml")
	}
	for name, want := range map[string]os.FileMode{"Dockerfile": 0o644, "entrypoint.sh": 0o755} {
		info, err := os.Stat(filepath.Join(app.Cwd, sandboxDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", name, info.Mode().Perm(), want)
		}
	}
	wantOut := "Initialized .sbx/ (per-project image override)\n" +
		"  - .sbx/Dockerfile\n" +
		"  - .sbx/entrypoint.sh\n"
	if out.String() != wantOut {
		t.Errorf("stdout = %q, want %q", out.String(), wantOut)
	}
}

func TestInitRefusesExistingDir(t *testing.T) {
	app, out, errb, _ := newApp(t)
	sb := filepath.Join(app.Cwd, sandboxDir)
	if err := os.Mkdir(sb, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(sb, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rc := app.cmdInit(); rc != 1 {
		t.Fatalf("cmdInit = %d, want 1", rc)
	}
	if want := ".sbx/ already exists; leaving it untouched.\n"; errb.String() != want {
		t.Errorf("stderr = %q, want %q", errb.String(), want)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("existing dir was modified: %v", err)
	}
}

func TestInitAtomicOnChmodFailure(t *testing.T) {
	app, _, errb, _ := newApp(t)
	orig := chmod
	chmod = func(string, os.FileMode) error { return fmt.Errorf("simulated chmod failure") }
	defer func() { chmod = orig }()

	if rc := app.cmdInit(); rc == 0 {
		t.Fatal("cmdInit = 0, want non-zero on chmod failure")
	}
	if _, err := os.Stat(filepath.Join(app.Cwd, sandboxDir)); !os.IsNotExist(err) {
		t.Errorf(".sbx/ should be removed; stat err = %v", err)
	}
	if !strings.Contains(errb.String(), "chmod") {
		t.Errorf("stderr = %q, want chmod error", errb.String())
	}
}

func TestInitReportsCleanupFailure(t *testing.T) {
	app, _, errb, _ := newApp(t)
	origW, origR := writeFile, removeAll
	writeFile = func(string, []byte, os.FileMode) error { return fmt.Errorf("write boom") }
	removeAll = func(string) error { return fmt.Errorf("cleanup boom") }
	defer func() { writeFile, removeAll = origW, origR }()

	if rc := app.cmdInit(); rc == 0 {
		t.Fatal("cmdInit = 0, want non-zero")
	}
	s := errb.String()
	if !strings.Contains(s, "write boom") || !strings.Contains(s, "cleanup boom") {
		t.Errorf("stderr = %q, want both errors", s)
	}
}

func TestDispatchRoutesAndVerbose(t *testing.T) {
	app, _, _, fr := newApp(t)
	fr.results = []runResult{{code: 0}} // image inspect: exists
	if rc := dispatch([]string{"-v", "run"}, app); rc != 0 {
		t.Fatalf("dispatch = %d, want 0", rc)
	}
	if !app.Verbose {
		t.Error("-v did not set Verbose")
	}
	if fr.calls[len(fr.calls)-1].Args[0] != "run" {
		t.Errorf("not routed to run: %v", fr.calls)
	}
}

func TestDispatchHelpAndVersion(t *testing.T) {
	app, out, errb, _ := newApp(t)
	if rc := dispatch([]string{"-h"}, app); rc != 0 || out.Len() == 0 || errb.Len() != 0 {
		t.Errorf("help: rc=%d outLen=%d errLen=%d", rc, out.Len(), errb.Len())
	}
	app2, out2, _, _ := newApp(t)
	if rc := dispatch([]string{"--version"}, app2); rc != 0 || out2.String() != version+"\n" {
		t.Errorf("version: rc=%d out=%q", rc, out2.String())
	}
}

func TestDispatchUsageErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"bogus"},
		{"--nope"},
		{"build", "extra"},
		{"ps", "x"},
		{"init", "x"},
		{"exec"},
		{"exec", "--"},
	}
	for _, args := range cases {
		app, _, errb, _ := newApp(t)
		if rc := dispatch(args, app); rc != 2 {
			t.Errorf("dispatch(%v) = %d, want 2", args, rc)
		}
		if errb.Len() == 0 {
			t.Errorf("dispatch(%v): expected usage on stderr", args)
		}
	}
}

func TestExecRunnerExitCodes(t *testing.T) {
	r := execRunner{}
	if code, nf := r.Run(CmdSpec{Name: "true", Silent: true}); code != 0 || nf {
		t.Errorf("true => (%d, %v), want (0, false)", code, nf)
	}
	if code, nf := r.Run(CmdSpec{Name: "false", Silent: true}); code != 1 || nf {
		t.Errorf("false => (%d, %v), want (1, false)", code, nf)
	}
}

func TestExecRunnerNotFound(t *testing.T) {
	code, nf := execRunner{}.Run(CmdSpec{Name: "sbx-no-such-binary-zzz", Silent: true})
	if !nf {
		t.Errorf("missing binary => (%d, %v), want notFound", code, nf)
	}
}

func TestExecRunnerSignalExitCode(t *testing.T) {
	code, nf := execRunner{}.Run(CmdSpec{Name: "sh", Args: []string{"-c", "kill -INT $$"}, Silent: true})
	if nf || code != 130 {
		t.Errorf("SIGINT => (%d, %v), want (130, false)", code, nf)
	}
}

func TestMergeEnvOverridesAndDedupes(t *testing.T) {
	base := []string{"PATH=/bin", "HOST_UID=999", "FOO=bar"}
	got := mergeEnv(base, []string{"HOST_UID=1000"})
	if slices.Contains(got, "HOST_UID=999") {
		t.Error("stale HOST_UID=999 not removed")
	}
	n := 0
	for _, e := range got {
		if strings.HasPrefix(e, "HOST_UID=") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("HOST_UID appears %d times, want 1", n)
	}
	for _, w := range []string{"HOST_UID=1000", "PATH=/bin", "FOO=bar"} {
		if !slices.Contains(got, w) {
			t.Errorf("missing %q", w)
		}
	}
}

func TestCmdRunBuildFails(t *testing.T) {
	app, _, _, fr := newApp(t)
	fr.results = []runResult{{code: 1}, {code: 5}} // image missing, build fails (5)
	if rc := app.cmdRun(nil); rc != 5 {
		t.Fatalf("cmdRun = %d, want 5 (build failure propagated)", rc)
	}
	if len(fr.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (inspect + build, no run)", len(fr.calls))
	}
	if fr.calls[1].Args[0] != "build" {
		t.Errorf("second call = %v, want build", fr.calls[1].Args)
	}
}

func TestCmdRunPosixGuardGetgid(t *testing.T) {
	app, _, errb, fr := newApp(t)
	orig := getgid
	getgid = func() int { return -1 }
	defer func() { getgid = orig }()
	if rc := app.cmdRun(nil); rc != 1 {
		t.Fatalf("cmdRun = %d, want 1", rc)
	}
	if len(fr.calls) != 0 {
		t.Errorf("runner called %d times, want 0", len(fr.calls))
	}
	if !strings.Contains(errb.String(), "POSIX") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestCmdDownEngineNotFound(t *testing.T) {
	app, _, _, fr := newApp(t)
	fr.results = []runResult{{notFound: true}}
	if rc := app.cmdDown(); rc != 127 {
		t.Fatalf("cmdDown = %d, want 127", rc)
	}
	if len(fr.calls) != 1 {
		t.Errorf("calls = %d, want 1", len(fr.calls))
	}
}

func TestCmdCleanStopsOnDownFailure(t *testing.T) {
	app, _, _, fr := newApp(t)
	fr.results = []runResult{{out: "id1"}, {code: 1}} // ps -aq -> id, rm fails
	if rc := app.cmdClean(); rc != 1 {
		t.Fatalf("cmdClean = %d, want 1 (down failure propagated)", rc)
	}
	if len(fr.calls) != 2 {
		t.Errorf("calls = %d, want 2 (ps + rm, no volume removal)", len(fr.calls))
	}
}

func TestCmdBuildNoOverrideWhenDirHasNoDockerfile(t *testing.T) {
	app, _, _, fr := newApp(t)
	if err := os.MkdirAll(filepath.Join(app.Cwd, sandboxDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if rc := app.cmdBuild(); rc != 0 {
		t.Fatalf("cmdBuild = %d, want 0", rc)
	}
	if got := lastCall(fr).Args; got[0] != "build" || got[2] != imageBase {
		t.Errorf("build args = %v, want base image %s", got, imageBase)
	}
}

func TestCmdBuildBaseContextFails(t *testing.T) {
	app, _, errb, fr := newApp(t)
	orig := mkdirTemp
	mkdirTemp = func(string, string) (string, error) { return "", fmt.Errorf("no temp dir") }
	defer func() { mkdirTemp = orig }()

	if rc := app.cmdBuild(); rc != 1 {
		t.Fatalf("cmdBuild = %d, want 1", rc)
	}
	if len(fr.calls) != 0 {
		t.Errorf("runner called %d times, want 0", len(fr.calls))
	}
	if !strings.Contains(errb.String(), "build:") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestExecRunnerStartError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "noexec")
	if err := os.WriteFile(f, []byte("not a program"), 0o644); err != nil { // no +x
		t.Fatal(err)
	}
	code, nf := execRunner{}.Run(CmdSpec{Name: f, Silent: true})
	if nf {
		t.Errorf("notFound = true, want false (file exists, just not executable)")
	}
	if code != 1 {
		t.Errorf("code = %d, want 1 for a non-exec start error", code)
	}
}
