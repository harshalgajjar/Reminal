// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// agentReloadsTools says whether a running agent fetches reminal's tool list
// again by itself when the server sends notifications/tools/list_changed, so
// a person does not have to restart it or reconnect the server to see tools
// that arrived after it started. name is the agent's name as integrate.go
// has it.
//
// True only where it was measured: the agent run against a stand-in server
// that adds a tool and sends the notification, and seen to ask for the list
// again and offer the new tool to its model. What an agent's documentation
// or source says is not enough (two such readings were wrong when
// measured), so an agent not measured yet is false: saying "restart it" to
// someone who did not need to costs less than leaving them without the
// tools.
func agentReloadsTools(name string) bool {
	switch name {
	case "Claude Code":
		// Measured, 2.1.330: asks again at once, and its next request to
		// the model names the new tool.
		return true
	case "OpenCode":
		// Measured, 1.19.6: asks again at once, and the new tool is in
		// its next request.
		return true
	}
	// Measured not to: Codex CLI 0.171.0 (does not ask again within a
	// minute; a prompt after that still offers only the old tools).
	// Not measured yet, and so false whatever is written of them: Gemini
	// CLI, Kilo Code CLI and Copilot CLI (their source or changelog says
	// they do), Qwen Code (its source has no handler), Cursor CLI, Amp and
	// Antigravity CLI (nothing found either way). pi has no MCP client:
	// reminal reaches it through an extension, which it loads as it starts.
	return false
}
