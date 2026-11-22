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
// tools. Where only part of that could be measured, the case says which
// part.
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
	case "Kilo Code CLI":
		// Measured, 7.8.32: asks again at once, and the new tool is in
		// its next request.
		return true
	case "Gemini CLI":
		// Measured, 0.70.0: asks again at once, and the new tool is in
		// its next request.
		return true
	case "Antigravity CLI":
		// Measured, 1.5.14, at its sign-in screen: asks again at once.
		// Signed out it makes no request to a model, so the new tool
		// being offered to its model could not be measured.
		return true
	case "Codex CLI":
		// Measured, 0.171.0: does not ask again within 60 s, and a
		// request after that still offers only the old tools.
		return false
	case "Qwen Code":
		// Measured, 0.28.4: does not ask again within 60 s, and its
		// requests after the change offer only the old tool.
		return false
	case "Copilot CLI":
		// Measured, 1.0.114, but only signed out: does not ask again
		// within 60 s. Signed in it may differ; that was not measured.
		return false
	case "Cursor CLI":
		// Not run: it needs a key to start. From source: it takes the
		// notification in, and nothing found there lists the tools again.
		return false
	case "Amp":
		// Could not be run (it needs a key to start), and its source
		// could not be read well enough to say. Not known.
		return false
	case "pi":
		// Measured, 1.6.0: pi's own MCP client does ask again at once.
		// But reminal joins pi through an extension, not MCP, and pi
		// loads that as it starts, so for reminal a running pi still
		// needs a restart.
		return false
	}
	// An agent nobody has measured.
	return false
}
