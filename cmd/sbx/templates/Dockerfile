FROM fedora:44

# System tools mise needs to fetch/build language runtimes. Everything else
# (node, python, go, ...) is left to mise and declared per project.
RUN dnf install -y git curl gcc make tar xz which && \
    dnf clean all

# Non-root user at uid/gid 1000; podman `keep-id:uid=1000,gid=1000` maps it back
# to the host user so bind-mounted files keep correct ownership.
RUN useradd -u 1000 -m appuser && \
    mkdir -p /home/appuser/.claude && chown appuser:appuser /home/appuser/.claude

COPY --chown=appuser entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

USER appuser

# mise manages tool versions; the Claude CLI is the agent itself.
RUN curl -fsSL https://mise.run | sh && \
    curl -fsSL https://claude.ai/install.sh | bash

# Put the Claude CLI and mise's shims on PATH so installed tools resolve without
# shell activation (shims are created by `mise install`).
ENV PATH="/home/appuser/.local/bin:/home/appuser/.local/share/mise/shims:$PATH"

WORKDIR /app
ENTRYPOINT ["/entrypoint.sh"]
CMD ["/usr/bin/bash"]
