#!/bin/bash
set -e

# krun microVMs boot the workload as root regardless of the image's USER. Drop to
# appuser (uid 1000) so the agent runs unprivileged and bind-mounted files map to
# the host user via podman's keep-id. (No-op when already appuser, e.g. plain runc.)
if [ "$(id -u)" -eq 0 ] && id -u appuser >/dev/null 2>&1; then
  # The named volumes mount in root-owned; hand them to appuser before dropping.
  chown appuser:appuser /home/appuser /home/appuser/.claude /home/appuser/.local/share/mise 2>/dev/null || true
  exec runuser -u appuser -- "$0" "$@"
fi

export HOME=/home/appuser
echo "Sandbox started as user: $(id -un) in directory: $(pwd)"

# Let git authenticate to github.com over HTTPS with the forwarded token FIRST,
# so mise/go can fetch private modules below. The token is read from the
# environment at credential time, never written to disk.
if [ -n "${GITHUB_TOKEN:-}" ]; then
  git config --global credential."https://github.com".helper \
    '!f() { test "$1" = get && printf "username=x-access-token\npassword=%s\n" "$GITHUB_TOKEN"; }; f'
fi

# Install the runtimes/tools a project declares via mise. Non-fatal: a failure
# (e.g. a private tool that can't be fetched) must not lock you out of the sandbox.
if [ -f mise.toml ] || [ -f .mise.toml ] || [ -f .config/mise/config.toml ] || [ -f .tool-versions ]; then
  mise trust --all || true
  # MISE_QUIET avoids the animated multi-progress renderer (which flickers, esp.
  # under tmux); errors still print. First run can be slow while tools install.
  echo "sbx: installing project tools via mise (first run may take a while)..." >&2
  MISE_QUIET=1 mise install || echo "sbx: warning: 'mise install' did not complete; continuing" >&2
fi

# Give Claude Code a temp dir owned by the current user, so its /tmp ownership
# check (which can trip under the user-namespace mapping) passes.
export CLAUDE_CODE_TMPDIR="$HOME/.cache/claude-tmp"
mkdir -p "$CLAUDE_CODE_TMPDIR"

exec "$@"
