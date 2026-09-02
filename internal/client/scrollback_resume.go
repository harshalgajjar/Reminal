// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// envResumeScrollback is the path of a 0600 dump written just before a hot
// restart. The session key is NOT carried across the exec (viewers re-EKE),
// so this file holds plaintext and the successor re-encrypts it.
const envResumeScrollback = "REMINAL_RESUME_SCROLLBACK"

const scrollbackDumpVersion = 1

type scrollbackDump struct {
	Version  int               `json:"v"`
	NextSeq  uint64            `json:"next_seq"`
	BaseCols int               `json:"base_cols"`
	BaseRows int               `json:"base_rows"`
	Entries  []scrollDumpEntry `json:"entries"`
}

type scrollDumpEntry struct {
	Seq  uint64 `json:"seq"`
	Data []byte `json:"data,omitempty"`
	Bar  bool   `json:"bar,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func scrollbackDumpPath(sessionID string) (string, error) {
	dir, err := reminalDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "scrollback-"+sessionID+".json"), nil
}

// writeScrollbackDump decrypts the live buffer and writes it to a 0600 file
// the successor will load. Empty history yields ("", nil) — no file.
func (a *Agent) writeScrollbackDump() (string, error) {
	if a == nil || a.buf == nil || a.box == nil || a.sessionID == "" {
		return "", nil
	}
	raw := a.buf.From(0)
	if len(raw) == 0 {
		return "", nil
	}
	dump := scrollbackDump{
		Version: scrollbackDumpVersion,
		NextSeq: a.buf.LatestSeq(),
	}
	dump.BaseCols, dump.BaseRows = a.buf.Base()
	dump.Entries = make([]scrollDumpEntry, 0, len(raw))
	for _, e := range raw {
		de := scrollDumpEntry{Seq: e.Seq, Bar: e.Bar, Cols: e.Cols, Rows: e.Rows}
		if e.Data != "" {
			pt, err := a.box.Decrypt(e.Data)
			if err != nil {
				return "", fmt.Errorf("decrypt scrollback seq %d: %w", e.Seq, err)
			}
			de.Data = pt
		}
		dump.Entries = append(dump.Entries, de)
	}
	path, err := scrollbackDumpPath(a.sessionID)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(dump)
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return "", err
	}
	// Windows rename refuses to replace an existing dest. A leftover from a
	// failed restart would then make every later dump fail open (no history).
	// Crash between this remove and rename leaves only .tmp; take/remove
	// delete that too.
	_ = os.Remove(path)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// dumpScrollbackForRestart writes the dump if there is history. A write
// error must not block restart — we notify and continue without a file.
func (a *Agent) dumpScrollbackForRestart() string {
	path, err := a.writeScrollbackDump()
	if err != nil {
		agentNotify("  reminal: could not save scrollback for restart — history will reset: %v\n", err)
		return ""
	}
	return path
}

// removeScrollbackDump deletes a dump and its .tmp sibling. Safe on "" / missing.
func removeScrollbackDump(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
	_ = os.Remove(path + ".tmp")
}

// takeScrollbackDump loads and deletes a dump written by the predecessor.
// Always deletes path and path+".tmp" so plaintext cannot linger after a
// corrupt / wrong-version file. Missing or unreadable files return nil so
// restart still comes up (just without history).
func takeScrollbackDump(path string) *scrollbackDump {
	if path == "" {
		return nil
	}
	body, err := os.ReadFile(path)
	removeScrollbackDump(path)
	if err != nil {
		return nil
	}
	var dump scrollbackDump
	if json.Unmarshal(body, &dump) != nil || dump.Version != scrollbackDumpVersion {
		return nil
	}
	if len(dump.Entries) == 0 {
		return nil
	}
	return &dump
}

// restoreResumedScrollback re-encrypts a predecessor dump under this agent's
// new session key and replays it into the snapshot emulator. Must run after
// initScreen and before the PTY pump, or live output interleaves with history.
func (a *Agent) restoreResumedScrollback() {
	d := a.resumeDump
	a.resumeDump = nil
	if d == nil || a.buf == nil || a.box == nil {
		return
	}
	entries := make([]scrollEntry, 0, len(d.Entries))
	for _, e := range d.Entries {
		se := scrollEntry{Seq: e.Seq, Bar: e.Bar, Cols: e.Cols, Rows: e.Rows}
		if len(e.Data) > 0 {
			enc, err := a.box.Encrypt(e.Data)
			if err != nil {
				agentNotify("  reminal: could not restore scrollback: %v\n", err)
				return
			}
			se.Data = enc
		}
		entries = append(entries, se)
	}
	a.buf.restore(entries, d.NextSeq, d.BaseCols, d.BaseRows)

	a.screenMu.Lock()
	defer a.screenMu.Unlock()
	if a.screen == nil {
		return
	}
	if d.BaseCols > 0 && d.BaseRows > 0 {
		resizeAnchoredBottom(a.screen, d.BaseCols, d.BaseRows)
		a.noteScrollbackLen()
	}
	for _, e := range d.Entries {
		if e.Cols > 0 && e.Rows > 0 {
			resizeAnchoredBottom(a.screen, e.Cols, e.Rows)
			a.noteScrollbackLen()
			continue
		}
		if len(e.Data) > 0 {
			_, _ = a.screen.Write(e.Data)
			a.noteScrollbackLen()
		}
	}
}
