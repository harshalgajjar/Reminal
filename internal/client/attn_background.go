// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"maps"
	"regexp"
	"strconv"
	"strings"

	"reminal/internal/session"
)

// The background work a harness still has running besides its shells and
// subagents (attnShellCount, its hook's background_tasks): by kind, as the
// harness names it ("monitor", "background process", "background work").

// attnMonitorPattern finds Claude Code's footer count of its monitors,
// beside its shells' ("· 1 shell, 2 monitors"): after the footer's separator
// or the comma it joins its kinds with.
var attnMonitorPattern = regexp.MustCompile(`[·,]\s*([0-9]+)\s*monitors?\b`)

// attnGeminiBackgroundPattern finds Gemini CLI's count of its background
// processes in its context summary ("1 GEMINI.md file · 1 Background
// process"): first in it, after its separator, or after the approval mode
// it shares the row with.
var attnGeminiBackgroundPattern = regexp.MustCompile(`(?i)(?:^|[\s·])([0-9]+)\s+background process`)

// attnGeminiStatusRows are the rows Gemini CLI draws its status in, just
// over its input box (Composer.tsx: StatusRow, then InputPrompt, then
// Footer): up from its prompt row, past its box's top edge (a row of ▄, or
// a rounded box's ╭──╮), at most three, and not past the rule above them.
// Under the box is its footer, which is where attnFooter reads: the status
// is not there, and attnPromptRows rows from the bottom do not reach it.
func attnGeminiStatusRows(tail string) []string {
	rows := strings.Split(strings.TrimRight(tail, "\n"), "\n")
	for i := len(rows) - 1; i > 0; i-- {
		if !attnPromptRow(rows[i]) {
			continue
		}
		j := i - 1
		if attnGeminiBoxEdge(rows[j]) {
			j--
		}
		var out []string
		for ; j >= 0 && len(out) < 3; j-- {
			if attnRuleRow(rows[j]) {
				break
			}
			out = append(out, rows[j])
		}
		return out
	}
	return nil
}

// attnGeminiBoxEdge says whether a row is the top of Gemini CLI's input box:
// a row of half blocks (▄), or a rounded box's top (attnRuleRow's ╭──╮).
func attnGeminiBoxEdge(row string) bool {
	r := strings.TrimSpace(stripANSI(row))
	return (r != "" && strings.Trim(r, "▄▀") == "") || (strings.HasPrefix(r, "╭") && attnRuleRow(row))
}

// attnCodexTerminalPattern finds Codex's count of the background terminals it
// still has running (bottom_pane/unified_exec_footer.rs: "1 background
// terminal running · /ps to view · /stop to close"): first in its row, or
// after the status it is joined to while a turn runs ("• Working (3s • esc
// to interrupt) · 1 background terminal running"). A narrow screen cuts the
// row's end, never its count.
var attnCodexTerminalPattern = regexp.MustCompile(`(?:^|·\s*)([0-9]+) background terminals? running`)

// attnCodexGap is the most rows Codex leaves between that row and its input
// box's prompt (two blank ones, as recorded).
const attnCodexGap = 3

// attnCodexActivityRow is the row Codex draws just over its input box: its
// status, or with none its background terminals' summary (bottom_pane/mod.rs).
// Its box is the lowest prompt (›) in tail; the row is the first written one
// above it, past the blank rows between. Under the box is its footer, where
// attnFooter reads, and its last attnPromptRows rows do not reach the row.
// "" with no prompt seen, or nothing written just over it.
func attnCodexActivityRow(tail string) string {
	rows := strings.Split(strings.TrimRight(tail, "\n"), "\n")
	for i := len(rows) - 1; i >= 0; i-- {
		if !strings.HasPrefix(strings.TrimSpace(stripANSI(rows[i])), "›") {
			continue
		}
		for j := i - 1; j >= 0 && i-j <= attnCodexGap; j-- {
			if r := strings.TrimSpace(stripANSI(rows[j])); r != "" {
				return r
			}
		}
		return ""
	}
	return ""
}

// attnOtherOnScreen is the background work besides its shells that prog's
// own screen counts: Claude Code's footer's monitors, Gemini CLI's
// background processes, Codex's background terminals. nil with no agent in
// front (attnShellCount's reason), or none shown.
func attnOtherOnScreen(prog string, agentActive bool, render string) map[string]int {
	if !agentActive {
		return nil
	}
	tail := attentionProbeTail(render, attnTailRows)
	first := func(re *regexp.Regexp, text string) int {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				return n
			}
		}
		return 0
	}
	var out map[string]int
	add := func(kind string, n int) {
		if n > 0 {
			if out == nil {
				out = map[string]int{}
			}
			out[kind] = n
		}
	}
	switch prog {
	case "claude":
		add("monitor", first(attnMonitorPattern, strings.ToLower(stripANSI(attnFooter(tail)))))
	case "gemini":
		for _, row := range attnGeminiStatusRows(tail) {
			if n := first(attnGeminiBackgroundPattern, strings.TrimSpace(stripANSI(row))); n > 0 {
				add("background process", n)
				break
			}
		}
	case "codex":
		add("background terminal", first(attnCodexTerminalPattern, attnCodexActivityRow(tail)))
	}
	return out
}

// attnBackground is what a harness still has running: its screen's counts
// and its hook's (bg, nil when it said nothing), the larger of the two for
// each kind — a hook that counts fewer than the screen shows never lowers
// it, nor the screen the hook. "background work" (a hook's word with no
// count, session.BackgroundWork) stands only while nothing else is counted.
func attnBackground(screenShells int, screenOther map[string]int, bg *session.Background) (shells, agents int, other map[string]int) {
	shells = screenShells
	other = maps.Clone(screenOther)
	if bg != nil {
		shells, agents = max(shells, bg.Shells), bg.Agents
		for k, n := range bg.Other {
			if n > other[k] {
				if other == nil {
					other = map[string]int{}
				}
				other[k] = n
			}
		}
	}
	if other[session.BackgroundWork] > 0 && shells+agents+session.CountOther(other)-other[session.BackgroundWork] > 0 {
		delete(other, session.BackgroundWork)
	}
	if len(other) == 0 {
		other = nil
	}
	return shells, agents, other
}
