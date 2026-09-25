# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar

# A throwaway box with pi in it, for exercising reminal's pi extension.
#
# The extension is TypeScript that pi loads, so the only honest test of it is a
# real pi discovering a real install and talking to a real `reminal mcp`. That
# needs pi on the machine — which is a lot to ask of anyone who just wants to
# build reminal, and it would run against their own pi config besides. This image
# gives the extension its own pi, its own home directory, and nothing of yours.
#
# See scripts/pi-test/run.sh for the one-command entry point.

FROM golang:1.26-bookworm

# pi ships as an npm package and wants a current Node.
ARG NODE_MAJOR=22
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        gnupg \
    && curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Unpinned on purpose: this rig exists to catch the day pi's extension API moves
# under us, which pinning would hide. run.sh prints the version it got.
ARG PI_VERSION=latest
RUN npm install -g "@earendil-works/pi-coding-agent@${PI_VERSION}" \
    && npm cache clean --force

ENV GOFLAGS=-buildvcs=false \
    PI_OFFLINE=1

COPY check.sh /usr/local/bin/check.sh
COPY probe.ts /opt/pi-test/probe.ts
RUN chmod +x /usr/local/bin/check.sh

WORKDIR /src
ENTRYPOINT ["/usr/local/bin/check.sh"]
