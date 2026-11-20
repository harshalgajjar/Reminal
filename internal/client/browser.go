// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"

	"reminal/internal/session"
)

// browserCallerOK says whether the process at the other end of the control
// socket may use this session's browser: one running inside the session (the
// session's own process, or one under it, as its agent and the agent's
// `reminal mcp` are). The person allows a session, so only what runs in it
// gets the browser: another program of the same user that names the session
// (REMINAL_SESSION is only an environment variable) does not. A variable for
// tests.
var browserCallerOK = func(peer int) bool { return session.DescendsFrom(peer, os.Getpid()) }

// handleBrowserControl is the control verb "browser <base64 of {tool, args}>":
// the session asks the daemon's browser hub as itself (its id, its name and
// this machine's, for the person's consent window), and answers with the
// base64 of the extension's answer ({ok, result|error}).
func (a *Agent) handleBrowserControl(conn net.Conn, b64 string) (string, error) {
	if conn != nil {
		peer, err := peerPID(conn)
		if err != nil {
			return "", errors.New("cannot tell which process is asking: " + err.Error())
		}
		if !browserCallerOK(peer) {
			return "", errors.New("only a program running in this session may use its browser")
		}
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", err
	}
	var in struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.Tool == "" {
		return "", errors.New("bad browser request")
	}
	a.metaMu.Lock()
	name := a.name
	a.metaMu.Unlock()
	machine, _ := os.Hostname()
	resp, err := AskBrowser(BrowserReq{Session: a.sessionID, Name: name, Machine: machine, Tool: in.Tool, Args: in.Args})
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out), nil
}
