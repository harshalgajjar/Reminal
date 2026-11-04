// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"reminal/internal/session"
)

// hookBackground reads, from a hook's payload, the work its harness still
// has running in the background. Claude Code's Stop and SubagentStop carry it
// as background_tasks: each an id, a type and a status. Only one whose status
// says running is counted, and every one that does, by its kind: "shell" a
// shell, "subagent" a subagent, any other type ("monitor", "workflow", "MCP
// task", "teammate", …) under its own name (Background.Other) — a kind
// reminal does not know yet is still work running, never counted as none.
// Qwen Code's Stop lists its background agents with no type, an agent_type
// each: subagents. An entry with neither is "background task". An entry
// that cannot be read is passed over. except is the subagent the event is
// about (SubagentStop's agent_id): it is finishing, whatever the list says
// of it.
//
// Antigravity's Stop says no list, only whether all of it is done
// (fullyIdle): not done stands as "background work", one of it, said with
// no count (session.BackgroundWork).
//
// nil when the payload says nothing of it at all — a harness, or an older
// build, that does not say — which leaves what the screen says to stand.
func hookBackground(payload []byte, except string) *session.Background {
	var p struct {
		Tasks     *[]json.RawMessage `json:"background_tasks"`
		FullyIdle *bool              `json:"fullyIdle"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return nil
	}
	if p.Tasks == nil {
		if p.FullyIdle == nil {
			return nil
		}
		if *p.FullyIdle {
			return &session.Background{}
		}
		return &session.Background{Other: map[string]int{session.BackgroundWork: 1}}
	}
	bg := &session.Background{}
	for _, raw := range *p.Tasks {
		var t struct {
			ID        json.RawMessage `json:"id"`
			Type      string          `json:"type"`
			AgentType string          `json:"agent_type"`
			Status    string          `json:"status"`
		}
		if json.Unmarshal(raw, &t) != nil || !strings.EqualFold(t.Status, "running") {
			continue
		}
		if except != "" && strings.Trim(string(t.ID), `"`) == except {
			continue
		}
		kind := strings.TrimSpace(t.Type)
		switch k := strings.ToLower(kind); {
		case k == "shell":
			bg.Shells++
		case k == "subagent" || (k == "" && t.AgentType != ""):
			bg.Agents++
		default:
			bg.AddOther(kind, 1) // an empty kind is session.BackgroundTask
		}
	}
	return bg
}

// hookAgentID is the subagent a SubagentStop is about.
func hookAgentID(payload []byte) string {
	var p struct {
		AgentID string `json:"agent_id"`
	}
	_ = json.Unmarshal(payload, &p)
	return p.AgentID
}

// hookDebugBackground says, with REMINAL_HOOK_DEBUG set, what a hook made of
// its event's background work: the counts, never the payload.
func hookDebugBackground(id, event string, bg *session.Background) {
	if os.Getenv("REMINAL_HOOK_DEBUG") == "" {
		return
	}
	said := "no background_tasks"
	if bg != nil {
		said = fmt.Sprintf("shells=%d agents=%d other=%d", bg.Shells, bg.Agents, bg.OtherCount())
	}
	_ = session.AppendHookDebug(fmt.Sprintf("session=%s event=%s %s", id, event, said))
}
