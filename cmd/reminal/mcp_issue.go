// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// report_issue: how an agent tells reminal that reminal got something wrong.
//
// Left to itself, an agent handed a wrong notice files it where its harness
// files things — Claude Code's own feedback queue, say — which reaches the
// harness's maker and never the person running reminal. This tool is the
// address: the report is kept on this machine (`reminal issues`). Nothing
// leaves the machine; a person exports and files it if they choose.

import (
	"fmt"
	"os"
	"strings"

	"reminal/internal/client"
	"reminal/internal/session"
)

func mcpIssueTool(obj func(map[string]any, ...string) map[string]any, str func(string) map[string]any) map[string]any {
	return map[string]any{
		"name": "report_issue",
		"description": "Report a problem with reminal ITSELF to the person running it: a tool that errors or misleads, " +
			"a transcript that is wrong, keys that did not land, a note that did not show. " +
			"The report is kept on this machine (`reminal issues` shows it); nothing is sent anywhere. " +
			"Use this — not your harness's own feedback or bug-report tools, which reach the harness's maker " +
			"and cannot fix reminal. Not for problems with the task you were given.",
		"inputSchema": obj(map[string]any{
			"title":         str("One line: what reminal got wrong."),
			"what_happened": str("What you saw, and what you did about it. Quote the notice or error if there was one."),
			"expected":      str("What should have happened instead (optional)."),
		}, "title", "what_happened"),
	}
}

func mcpReportIssue(title, what, expected string) (string, error) {
	title, what = strings.TrimSpace(title), strings.TrimSpace(what)
	if title == "" || what == "" {
		return "", fmt.Errorf("report_issue needs a title and what_happened")
	}
	sid := strings.ToUpper(strings.TrimSpace(os.Getenv("REMINAL_SESSION")))
	if sid == "" {
		sid = strings.ToUpper(session.Enclosing())
	}
	is := client.Issue{Session: sid, Title: title, What: what, Expected: strings.TrimSpace(expected),
		Version: version}
	if sid != "" {
		is.Screen = client.OwnScreenTail(sid, 60)
	}
	path, err := client.SaveIssue(is)
	if err != nil {
		return "", err
	}
	out := fmt.Sprintf("Reported to reminal — not to your harness. Kept at %s; `reminal issues` on this machine lists it and "+
		"`reminal issues export` turns it into a bug report the person can file. Nothing was sent anywhere.", path)
	return out, nil
}
