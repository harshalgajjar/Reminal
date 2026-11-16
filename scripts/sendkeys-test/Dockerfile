# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar

# A box whose sessions start zsh, which turns bracketed paste on by itself:
# what send_keys has to submit a line in front of. reminal is built by run.sh.
FROM debian:trixie-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends zsh procps ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY reminal /usr/local/bin/reminal
# A .zshrc of its own, so zsh does not stop at its first-run setup.
RUN echo "PS1='%# '" > /root/.zshrc
ENV HOME=/root SHELL=/bin/zsh REMINAL_RELAY=ws://127.0.0.1:8080/ws REMINAL_WEB=http://127.0.0.1:8080
CMD ["sh", "-c", "reminal relay 8080 >/root/relay.log 2>&1 & sleep 1; reminal daemon >/root/daemon.log 2>&1 & exec sleep infinity"]
