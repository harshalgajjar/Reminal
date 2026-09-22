// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"reminal/internal/crypto"
	"reminal/internal/protocol"
)

// UpgradeTimeout bounds one machine's upgrade end to end. Generous: it covers
// downloading a release over a slow link, not just the handshake.
const UpgradeTimeout = 5 * time.Minute

// upgradeStageWait bounds the gap BETWEEN progress steps. The host narrates as
// it goes, so a long silence means it died rather than that it is still working.
const upgradeStageWait = 90 * time.Second

// signOwnerAction produces the proof a privileged action needs: this device's
// enrolled owner key signing (session, action, nonce, timestamp). It is the
// producer side of verifyOwnerAction — until now only the web viewer built one,
// so the CLI could not drive an owner-gated action at all.
//
// sessionID binds the proof to the channel it is sent on; for a machine channel
// that is the derived directory id, which is what the host verifies against.
func signOwnerAction(sessionID, action string) (ownerProof, error) {
	deviceKey, err := loadOrCreateDeviceKey()
	if err != nil {
		return ownerProof{}, err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return ownerProof{}, err
	}
	unix := time.Now().Unix()
	sig := crypto.SignOwner(deviceKey, crypto.OwnerActionTranscript(sessionID, action, nonce, unix))
	return ownerProof{
		DevicePub: base64.StdEncoding.EncodeToString(deviceKey.Public().(ed25519.PublicKey)),
		DeviceSig: base64.StdEncoding.EncodeToString(sig),
		Nonce:     base64.StdEncoding.EncodeToString(nonce),
		Unix:      unix,
	}, nil
}

// UpgradeStep is one narrated step of a machine's upgrade, as the host reports
// it. Stage is download | verify | install | restart | done.
type UpgradeStep struct {
	Stage   string
	Pct     int
	Detail  string
	Version string
	Error   string
}

// UpgradeOnMachine asks an owned machine to upgrade itself, over the same
// owner-authenticated machine channel the web Machines panel uses, and relays
// the host's own narration to onStep as it arrives.
//
// The host publishes its LAST step before it restarts anything (deliberately —
// every agent is re-exec'd on the next line), so the final step we see is the
// real outcome even though the connection then drops under us. A drop after
// progress is therefore success-shaped, not an error.
func UpgradeOnMachine(machinePub ed25519.PublicKey, onStep func(UpgradeStep)) (UpgradeStep, error) {
	var last UpgradeStep
	conn, box, err := dialDirectoryOwner(machinePub, DirectoryTimeout)
	if err != nil {
		return last, err
	}
	defer conn.Close()

	// The host verifies this against the channel it arrived on.
	dirID := crypto.DeriveDirectoryID(machinePub)
	proof, err := signOwnerAction(dirID, "upgrade")
	if err != nil {
		return last, err
	}
	body, err := json.Marshal(proof)
	if err != nil {
		return last, err
	}
	enc, err := box.Encrypt(body)
	if err != nil {
		return last, err
	}
	if err := writeDir(conn, protocol.Message{Type: protocol.TypeUpgrade, Data: enc}); err != nil {
		return last, err
	}

	deadline := time.Now().Add(UpgradeTimeout)
	seen := false
	for {
		wait := time.Now().Add(upgradeStageWait)
		if wait.After(deadline) {
			wait = deadline
		}
		_ = conn.SetReadDeadline(wait)
		var msg protocol.Message
		if err := readDir(conn, &msg); err != nil {
			if seen {
				// The host narrated, then went — that is the restart, not a
				// failure. The last step it published is the outcome.
				return last, nil
			}
			return last, err
		}
		if msg.Type != protocol.TypeUpgrade {
			continue
		}
		plain, derr := box.Decrypt(msg.Data)
		if derr != nil {
			continue
		}
		var st upgradeStage
		if json.Unmarshal(plain, &st) != nil {
			continue
		}
		last = UpgradeStep{Stage: st.Stage, Pct: st.Pct, Detail: st.Detail, Version: st.Version, Error: st.Error}
		seen = true
		if onStep != nil {
			onStep(last)
		}
		if st.Error != "" {
			return last, fmt.Errorf("%s", st.Error)
		}
		if st.Stage == "done" {
			return last, nil
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("upgrade timed out after %s", UpgradeTimeout)
		}
	}
}

// RestartOnMachine asks an owned machine to hot-restart every session on it —
// the remote counterpart to `reminal restart --all`. Shells and everything
// running in them survive: a hot restart re-execs each agent while keeping its
// PTY. Returns how many sessions the host moved.
//
// ok is false when the host listed but did not restart (a reminal too old to
// understand the request), so a silent no-op can never read as success.
func RestartOnMachine(machinePub ed25519.PublicKey, timeout time.Duration) (count int, ok bool, err error) {
	resp, err := queryDirectory(machinePub, timeout, dirQueryReq{Restart: true})
	if err != nil {
		return 0, false, err
	}
	if resp.RestartError != "" {
		return resp.RestartCount, false, fmt.Errorf("%s", resp.RestartError)
	}
	return resp.RestartCount, resp.RestartOK, nil
}
