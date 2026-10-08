// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"reminal/internal/session"
)

// agentConv is the conversation the agent running as pid in this session is
// in: what the agent itself says about that process when it says, or what
// its hook last reported — whichever is newer.
//
// The hook alone was not enough. It reports only when a hook fires, so an
// agent that had not been sent anything since it started had no id at all,
// and a restart reopened the agent's list instead of its conversation.
func agentConv(prog string, pid int, sessionID string) string {
	conv, at := session.ReadConvAt(sessionID)
	if f, ok := liveConvs[prog]; ok {
		if live, liveAt := f(pid); live != "" && (conv == "" || !liveAt.Before(at)) {
			return live
		}
	}
	return conv
}

// liveConvs: for an agent that keeps a record of each of its running
// processes, how to read the conversation one process is in.
var liveConvs = map[string]func(pid int) (string, time.Time){
	"claude": claudeLiveConv,
}

// liveConvRe is what a conversation id read from an agent's own files may
// look like: it is typed into a shell on restore.
var liveConvRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,127}$`)

// claudeLiveConv reads Claude Code's record of a running process,
// <config>/sessions/<pid>.json, which names the conversation that process is
// in and is rewritten when it changes. The record must be for that pid.
func claudeLiveConv(pid int) (string, time.Time) {
	if pid <= 0 {
		return "", time.Time{}
	}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", time.Time{}
		}
		dir = filepath.Join(home, ".claude")
	}
	f, err := os.Open(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return "", time.Time{}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", time.Time{}
	}
	var rec struct {
		PID       int    `json:"pid"`
		SessionID string `json:"sessionId"`
	}
	if json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&rec) != nil || rec.PID != pid || !liveConvRe.MatchString(rec.SessionID) {
		return "", time.Time{}
	}
	return rec.SessionID, fi.ModTime()
}
