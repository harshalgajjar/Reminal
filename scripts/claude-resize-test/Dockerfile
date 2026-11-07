# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
# A box with the real Claude Code at the pinned version, the stand-in model,
# and this tree's relay (which serves the viewer) and agent.
FROM node:22-bookworm
ARG CLAUDE_VERSION
RUN npm i -g --silent @anthropic-ai/claude-code@${CLAUDE_VERSION} >/dev/null 2>&1 && claude --version
COPY relay /usr/local/bin/reminal-relay
COPY agent /usr/local/bin/reminal
COPY model.mjs setup.sh /opt/rig/
# The session's shell is Claude Code itself, with the model's environment.
RUN printf '#!/bin/bash\n. /etc/profile.d/model.sh\nexec claude\n' > /usr/local/bin/claude-shell && chmod +x /usr/local/bin/claude-shell
# The story streams for half a minute (a few words every 150 ms), so the
# screen changes land while frames are in flight, not on an idle Claude.
ENV HOME=/root REMINAL_RELAY=ws://127.0.0.1:18080/ws REMINAL_WEB=http://127.0.0.1:18080 REMINAL_KEYSTORE=file MODEL_DELTA_MS=150
CMD ["sh", "-c", "node /opt/rig/model.mjs >/tmp/model.log 2>&1 & bash /opt/rig/setup.sh && reminal-relay relay 18080 >/tmp/relay.log 2>&1 & exec sleep infinity"]
