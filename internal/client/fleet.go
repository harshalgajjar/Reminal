// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"fmt"
	"strings"
	"sync"

	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
)

// FleetMachine is one machine this device owns, plus the live sessions it
// reported. Local is served from the registry (no relay); remotes are a
// directory query. SearchHits on a session are filled only when CollectFleet
// was called with a pattern and that host knows how to search.
type FleetMachine struct {
	Key      ed25519.PublicKey
	ID       string
	ShortID  string
	Name     string
	Hostname string
	Local    bool
	Online   bool
	Error    string
	Sessions []protocol.DirSession
}

// CollectFleet lists every machine this device owns and the sessions on each.
// The machine we are sitting at is always included. search, when set, is
// forwarded to remote directory hosts (older hosts ignore it and still list).
func CollectFleet(search string) ([]FleetMachine, error) {
	machines, err := ListOwnedMachines()
	if err != nil {
		return nil, err
	}
	localKey, _ := MachinePub()
	var remote []OwnedMachine
	localOM := OwnedMachine{Key: localKey}
	for _, m := range machines {
		if localKey != nil && m.Key.Equal(localKey) {
			localOM = m
			continue
		}
		remote = append(remote, m)
	}

	type remRes struct {
		m    OwnedMachine
		resp protocol.DirResponse
		err  error
	}
	remoteResults := make([]remRes, len(remote))
	var wg sync.WaitGroup
	for i, m := range remote {
		wg.Add(1)
		go func(i int, m OwnedMachine) {
			defer wg.Done()
			resp, qerr := queryDirectory(m.Key, DirectoryTimeout, dirQueryReq{Pattern: search})
			remoteResults[i] = remRes{m: m, resp: resp, err: qerr}
		}(i, m)
	}
	wg.Wait()

	var out []FleetMachine
	if localKey != nil {
		out = append(out, fleetFrom(localOM, LocalDirectory(), nil, true))
	}
	for _, r := range remoteResults {
		out = append(out, fleetFrom(r.m, r.resp, r.err, false))
	}
	return out, nil
}

func fleetFrom(m OwnedMachine, resp protocol.DirResponse, err error, local bool) FleetMachine {
	fm := FleetMachine{
		Key:      m.Key,
		ID:       MachineID(m.Key),
		ShortID:  ShortMachineID(m.Key),
		Hostname: resp.Hostname,
		Local:    local,
		Online:   err == nil,
		Sessions: resp.Sessions,
	}
	if err != nil {
		fm.Error = err.Error()
		fm.Online = false
	}
	switch {
	case m.Name != "":
		fm.Name = m.Name
	case fm.Online && resp.Hostname != "":
		fm.Name = resp.Hostname
	default:
		fm.Name = fm.ShortID
	}
	return fm
}

// applyLocalSearchHits fills SearchHits on sessions whose local agent answers
// the control-socket search. A miss or a down socket leaves the session listed
// without hits — the directory reply must still arrive.
func applyLocalSearchHits(resp *protocol.DirResponse, pattern string) {
	if resp == nil || pattern == "" {
		return
	}
	all, err := session.ReadAllActive()
	if err != nil {
		return
	}
	pidByID := make(map[string]int, len(all))
	for _, a := range all {
		if a.IsPort() || a.PID <= 0 {
			continue
		}
		pidByID[a.ID] = a.PID
	}
	for i := range resp.Sessions {
		pid := pidByID[resp.Sessions[i].ID]
		if pid == 0 {
			continue
		}
		hits, err := SearchAgentTranscript(pid, pattern)
		if err != nil || len(hits) == 0 {
			continue
		}
		resp.Sessions[i].SearchHits = hits
	}
}

// applyLocalTranscriptDump fills the named session's Transcript fields via
// the control socket. Unknown / port / failed dumps still leave the session
// listed; TranscriptOK is only set when the agent answered.
func applyLocalTranscriptDump(resp *protocol.DirResponse, sessionID string) {
	if resp == nil || sessionID == "" {
		return
	}
	all, err := session.ReadAllActive()
	if err != nil {
		return
	}
	var pid int
	for _, a := range all {
		if strings.EqualFold(a.ID, sessionID) {
			if a.IsPort() || a.PID <= 0 {
				return
			}
			pid = a.PID
			sessionID = a.ID
			break
		}
	}
	if pid == 0 {
		return
	}
	text, truncated, err := ReadAgentTranscript(pid)
	if err != nil {
		return
	}
	for i := range resp.Sessions {
		if resp.Sessions[i].ID != sessionID {
			continue
		}
		resp.Sessions[i].Transcript = text
		resp.Sessions[i].TranscriptTruncated = truncated
		resp.Sessions[i].TranscriptOK = true
		return
	}
}

// ReadAgentTranscriptFromID dumps a local session by id via its control socket.
func ReadAgentTranscriptFromID(id string) (string, bool, error) {
	a, err := session.ReadActiveByID(id)
	if err != nil {
		return "", false, err
	}
	if a.IsPort() {
		return "", false, fmt.Errorf("session %s is a port forward — no terminal transcript", a.ID)
	}
	return ReadAgentTranscript(a.PID)
}

// ReadRemoteTranscript asks an owned machine's directory host for one
// session's stripped scrollback. ok is false when the host listed the
// session but did not dump (older reminal).
func ReadRemoteTranscript(machinePub ed25519.PublicKey, sessionID string) (text string, truncated, ok bool, err error) {
	resp, err := queryDirectory(machinePub, DirectoryTimeout, dirQueryReq{Transcript: sessionID})
	if err != nil {
		return "", false, false, err
	}
	for _, s := range resp.Sessions {
		if strings.EqualFold(s.ID, sessionID) {
			return s.Transcript, s.TranscriptTruncated, s.TranscriptOK, nil
		}
	}
	return "", false, false, fmt.Errorf("session %s not found on that machine", sessionID)
}
