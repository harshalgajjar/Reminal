// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"

	"github.com/gorilla/websocket"

	"reminal/internal/protocol"
	"reminal/internal/updater"
)

// SwitchChannelAction is what an owner signs to ask this machine to switch
// channel. The channel and the manifest are inside the signature, so a proof
// made for one switch cannot be carried over to point the machine somewhere
// else — which matters most where builds are unsigned and nothing downstream
// could tell a foreign one from ours.
func SwitchChannelAction(channel, manifest string) string {
	return "switch-channel\n" + channel + "\n" + manifest
}

// handleSwitchChannel moves this machine onto another channel's newest build
// and hot-restarts every session onto it, exactly as an upgrade does: one at a
// time per machine, narrated to everyone watching, this session last, and
// everything running in the shells kept. Only the machine channel serves it —
// it replaces the binary under every session here, so a session's PIN guests
// must never reach it.
func (a *Agent) handleSwitchChannel(conn *websocket.Conn, data string) {
	if a.box == nil {
		return
	}
	var req struct {
		Channel  string     `json:"channel"`
		Manifest string     `json:"manifest"`
		Proof    ownerProof `json:"proof"`
	}
	if data != "" {
		if pt, err := a.box.Decrypt(data); err == nil {
			_ = json.Unmarshal(pt, &req)
		}
	}
	refuse := func(why string) {
		a.sendWindowMsg(conn, protocol.TypeUpgrade, upgradeStage{Stage: "download", Error: why, Version: a.version})
	}
	if !a.machine {
		refuse("a switch is asked of a machine, not of one of its sessions")
		return
	}
	if req.Channel == "" || req.Manifest == "" {
		refuse("no channel, or no manifest, was named")
		return
	}
	if why := a.verifyOwnerAction(req.Proof, SwitchChannelAction(req.Channel, req.Manifest)); why != "" {
		refuse(why)
		return
	}
	a.replaceAndRestart(conn, "Fetching the newest "+req.Channel+" build", func() (bool, error) {
		if req.Channel == updater.ChannelName() {
			return false, nil // already there
		}
		_, err := updater.SwitchChannel(req.Manifest, req.Channel)
		return err == nil, err
	})
}
