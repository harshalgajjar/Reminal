// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"

	"reminal/internal/protocol"
)

// Element-level control of a machine this device owns, for `reminal mcp`: an
// agent on one machine reads and operates apps on another through the same
// owner channel the viewer uses. Snapshots can hold whatever is on that screen,
// so they travel only inside the channel's end-to-end encryption, never as
// plaintext through the relay.

// DesktopTimeout bounds one remote request; an app's first snapshot is slow.
const DesktopTimeout = axCeiling + 10*time.Second

// DesktopWindows lists the windows on machinePub (nil: this machine).
func DesktopWindows(machinePub ed25519.PublicKey) ([]DesktopWindow, error) {
	var wins []winInfo
	if machinePub == nil {
		b := newWindowBackend()
		if reason := b.unsupported(); reason != "" {
			return nil, errors.New(reason)
		}
		var err error
		if wins, err = b.list(); err != nil {
			return nil, err
		}
	} else {
		var payload struct {
			Windows     []winInfo `json:"windows"`
			Unsupported string    `json:"unsupported"`
			Error       string    `json:"error"`
		}
		if err := machineRoundTrip(machinePub, protocol.TypeWindowList, nil, &payload, func() bool { return true }); err != nil {
			return nil, err
		}
		if payload.Unsupported != "" {
			return nil, errors.New(payload.Unsupported)
		}
		if payload.Error != "" {
			return nil, errors.New(payload.Error)
		}
		wins = payload.Windows
	}
	out := make([]DesktopWindow, 0, len(wins))
	for _, w := range wins {
		out = append(out, DesktopWindow{ID: w.ID, App: w.App, Title: w.Title, W: w.W, H: w.H})
	}
	return out, nil
}

// DesktopWindow is one window as `reminal mcp` reports it.
type DesktopWindow struct {
	ID    string `json:"window_id"`
	App   string `json:"app"`
	Title string `json:"title"`
	W     int    `json:"w"`
	H     int    `json:"h"`
}

// DesktopRequest is one element-level request, as axReq.
type DesktopRequest = axReq

// DesktopReply is its answer, as axReply.
type DesktopReply = axReply

// Desktop runs one request on machinePub (nil: this machine).
func Desktop(machinePub ed25519.PublicKey, req DesktopRequest) (DesktopReply, error) {
	if machinePub == nil {
		if runtime.GOOS == "darwin" {
			return mirrorForwardAX(req), nil
		}
		b := newWindowBackend()
		if reason := b.unsupported(); reason != "" {
			return DesktopReply{}, errors.New(reason)
		}
		return desktopAX.serve(b, req), nil
	}
	var seq [4]byte
	_, _ = rand.Read(seq[:])
	req.Seq = int(binary.BigEndian.Uint32(seq[:]) >> 1)
	var rep axReply
	err := machineRoundTrip(machinePub, protocol.TypeWindowAX, req, &rep, func() bool { return rep.Seq == req.Seq })
	return rep, err
}

// machineRoundTrip sends one encrypted request of type t to machinePub's owner
// channel and decodes the first reply of the same type that mine accepts.
func machineRoundTrip(machinePub ed25519.PublicKey, t protocol.MessageType, body any, out any, mine func() bool) error {
	conn, box, err := dialDirectoryOwner(machinePub, DesktopTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	msg := protocol.Message{Type: t}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		if msg.Data, err = box.Encrypt(raw); err != nil {
			return err
		}
	}
	if err := writeDir(conn, msg); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(DesktopTimeout))
	for {
		var in protocol.Message
		if err := readDir(conn, &in); err != nil {
			return fmt.Errorf("no answer from that machine: %w", err)
		}
		if in.Type != t {
			continue
		}
		plain, err := box.Decrypt(in.Data)
		if err != nil {
			return errors.New("directory: decrypt failed")
		}
		if err := json.Unmarshal(plain, out); err == nil && mine() {
			return nil
		}
	}
}
