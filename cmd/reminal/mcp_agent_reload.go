// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// agentReloadsTools says whether a running agent fetches reminal's tool list
// again by itself when the server sends notifications/tools/list_changed, so
// a person does not have to restart it or reconnect the server to see tools
// that arrived after it started. name is the agent's name as integrate.go
// has it.
//
// True only where the agent's own documentation, changelog or source says
// so (read 22 November 2026). An agent that is not named here, or for which
// the evidence is missing, weak or disputed, is false: saying "restart it"
// to someone who did not need to costs less than leaving them without the
// tools.
func agentReloadsTools(name string) bool {
	switch name {
	case "Claude Code":
		// Documentation, "Dynamic tool updates": an interactive session
		// fetches the updated list, with no reconnect.
		// https://code.claude.com/docs/en/mcp
		return true
	case "Gemini CLI":
		// Source: the handler for the notification calls refreshTools, and
		// is set whether or not the server declared listChanged.
		// packages/core/src/tools/mcp-client.ts in google-gemini/gemini-cli
		return true
	case "Kilo Code CLI":
		// Source: the handler lists the tools again and replaces the ones
		// it held. packages/opencode/src/mcp/index.ts in its repository.
		return true
	case "Copilot CLI":
		// Changelog: 0.0.354 "Support for MCP server tool notifications",
		// 1.0.71 "Keep MCP tool lists up to date when servers change",
		// 1.0.85 the same turn. changelog.md in github/copilot-cli
		return true
	case "pi":
		// Documentation: "When a server announces a changed tool list, new
		// tools are added and withdrawn tools become unreachable."
		// packages/coding-agent/docs/mcp.md in earendil-works/pi
		return true
	}
	// Not shown to reload:
	//   Codex CLI: the refresh is behind a feature that is off by default
	//     (mcp_tool_list_changed), and openai/codex issue 37417 is open.
	//   OpenCode: has a handler, but anomalyco/opencode issue 64117 (open)
	//     reports the new tools not reaching the model.
	//   Qwen Code: no handler in the terminal client; QwenLM/qwen-code
	//     issue 13632 is open.
	//   Cursor CLI, Amp, Antigravity CLI: nothing official either way.
	return false
}
