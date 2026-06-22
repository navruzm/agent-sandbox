# agent-sandbox

[![CI](https://github.com/navruzm/agent-sandbox/actions/workflows/ci.yml/badge.svg)](https://github.com/navruzm/agent-sandbox/actions/workflows/ci.yml)

`sbx` — a tiny CLI that runs coding agents (e.g. Claude Code) inside a disposable,
isolated sandbox so they can work autonomously without touching your host.

By default the sandbox is a **podman + krun microVM**, and `sbx` needs **no per-project
setup** — just `cd project && sbx run`. Each run is a fresh `--rm` container with the
current directory bind-mounted at `/app`. Language runtimes are managed by
[`mise`](https://mise.jdx.dev) (declared per project in `mise.toml` / `.tool-versions`),
so nothing language-specific is baked into the image.

## Requirements

- Go ≥ 1.26 to build (or [`mise`](https://mise.jdx.dev), which installs the pinned toolchain)
- **podman** (default) or **docker** (`SBX_ENGINE=docker`) — on Linux, or on macOS
  (where both run containers in a managed Linux VM)

## Install

```bash
mise run build && cp sbx ~/.local/bin/   # or, without mise: go build -o sbx ./cmd/sbx
```

## Usage

```bash
sbx run                # start the sandbox; with no command, runs the agent
                       #   autonomously (claude --dangerously-skip-permissions)
sbx run -- bash        # drop into a shell instead
sbx run -- <command>   # run a one-off command, e.g. sbx run -- claude --resume
sbx exec -- <command>  # run a command in an already-running sandbox
sbx build              # (re)build the image
sbx init               # customize the image (writes .sbx/Dockerfile)
sbx ps                 # list this project's sandbox containers
sbx down               # stop and remove them
sbx clean              # also remove this project's tool-cache volume
```

- `sbx run` with no command launches the agent with permission prompts off — safe
  because the microVM is the isolation boundary. Use `sbx run -- bash` for a shell.
- Run `sbx run` multiple times in one project for **concurrent instances**; `ps` lists
  them, `exec` attaches to the most recent, `down` removes them all.
- Put `-v`/`--verbose` before the command to echo the underlying engine commands.

## Configuration

All optional, via environment variables:

| Variable                   | Effect                                                                                                                          |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| `SBX_ENGINE=docker`        | Use docker instead of podman (plain container, **no microVM**).                                                                 |
| `SBX_CPUS` / `SBX_RAM_MIB` | Sandbox CPU / RAM limits (default `4` / `4096`).                                                                                |
| `SBX_READONLY_GIT=1`       | Mount `.git` read-only — stops the agent from planting a git hook that would run on your host (also blocks in-sandbox commits). |
| `SBX_ISOLATED_CONFIG=1`    | Give this directory its own Claude credentials instead of the shared login.                                                     |
| `GITHUB_TOKEN`             | Forwarded into the sandbox for private-repo access over HTTPS (see below).                                                      |
| `GOPRIVATE`                | Forwarded so `go`/mise fetch private modules directly (e.g. `github.com/acme/*`).                                               |

## Customizing the image

Run `sbx init` to scaffold an editable `.sbx/Dockerfile` + `entrypoint.sh`;
that directory's image is then used instead of the shared one. `sbx` searches **up the
directory tree** (stopping at `$HOME`) for the nearest `.sbx/`, so a config at
a parent directory applies to all repos beneath it:

```
~/work/acme/.sbx/   ← sbx init here
~/work/acme/api/    ← uses acme's image + credentials
~/work/acme/web/    ← same
```

Image **and Claude credentials** follow that scope (so all repos under it share one
login); the container identity and tool cache stay per-directory.

## Private repositories

`sbx` forwards your `GITHUB_TOKEN` into the sandbox and configures git to use it for
`github.com`, so cloning/fetching private repos over HTTPS just works. The token is
passed by reference (`-e GITHUB_TOKEN`), so its value never appears on a command line or
in `ps`. Because the agent runs with prompts off, use a **fine-grained, read-only PAT**.
For private Go modules, also set `GOPRIVATE`. Commit signing is off inside the sandbox.

## Engines

- **podman** (default) gives the krun microVM, `keep-id` host-UID mapping (so files the
  agent creates are owned by you), and recursive volume `chown`. libkrun/krun is
  cross-platform — KVM on Linux, HVF on macOS/Apple Silicon.
- **docker** (`SBX_ENGINE=docker`) is a best-effort fallback: a plain container (no
  microVM), using `--user` and SELinux relabeling. Prefer rootless Docker for correct
  file ownership.

**macOS:** the `sbx` binary runs natively, and podman/docker run containers inside a
managed Linux VM (`podman machine` / Docker Desktop), so `sbx` works there too. The
per-container krun annotation is a no-op in that path (the VM is the isolation boundary),
and the `keep-id`/`:U` ownership tricks are Linux-oriented — so macOS is best-effort.
(The binary refuses only where the OS has no host UID/GID, e.g. Windows.)

## Development

This repo uses [`mise`](https://mise.jdx.dev) — the same tool the sandbox uses — to pin
the Go toolchain and run tasks:

```bash
mise run check     # gofmt check + golangci-lint + go test
mise run lint      # golangci-lint only
mise run build     # build ./sbx
mise tasks         # list all tasks
```

Without mise, plain Go works too: `go test ./...`, `go build -o sbx ./cmd/sbx`.

## License

MIT — see [LICENSE](LICENSE).
