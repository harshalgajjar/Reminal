#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# First boot of the box: Claude Code pointed at model.mjs, its onboarding
# done and the folder trusted, as a person who uses it has, so a session
# opens at its prompt and nothing is typed into a first-run screen.
set -eu
KEY=sk-ant-not-a-secret-0000000000000000000000000000
cat > /etc/profile.d/model.sh <<EOP
export ANTHROPIC_BASE_URL=http://127.0.0.1:8099 ANTHROPIC_API_KEY=$KEY
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 DISABLE_AUTOUPDATER=1 IS_SANDBOX=1
EOP
# A folder a session, each trusted (run.sh opens story0 and story1).
mkdir -p "$HOME/story" "$HOME/story0" "$HOME/story1"
node -e '
const k=process.argv[1]; const fs=require("fs");
const trusted = {hasTrustDialogAccepted:true, hasCompletedProjectOnboarding:true, allowedTools:[]};
fs.writeFileSync(process.env.HOME+"/.claude.json", JSON.stringify({hasCompletedOnboarding:true, lastOnboardingVersion:"2.1.319", theme:"dark", numStartups:5, firstStartTime:"2026-11-01T00:00:00.000Z", migrationVersion:14, opusProMigrationComplete:true, sonnet1m45MigrationComplete:true, hasResetAutoModeOptInForDefaultOffer:true, hasSeenAutoDefaultNotice:true, fullscreenUpsellSeenCount:99, lastReleaseNotesSeen:"2.1.319",  bypassPermissionsModeAccepted:true,
  customApiKeyResponses:{approved:[k.slice(-20)], rejected:[]},
  projects:Object.fromEntries(["story","story0","story1"].map(d=>[process.env.HOME+"/"+d, trusted]))}, null, 2));' "$KEY"
mkdir -p "$HOME/.claude"
echo '{"skipDangerousModePermissionPrompt":true}' > "$HOME/.claude/settings.json"
