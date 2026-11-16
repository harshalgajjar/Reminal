// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"testing"
)

func TestPrepareInjectKeysSplit(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		enter    bool
		wantBody string
		wantTail string
	}{
		{
			name:     "enter is split off so the text lands as its own chunk",
			in:       "hello",
			enter:    true,
			wantBody: "hello",
			wantTail: "\r",
		},
		{
			name:     "no enter asked for, nothing to split",
			in:       "hello",
			enter:    false,
			wantBody: "hello",
			wantTail: "",
		},
		{
			// The caller's own trailing newline is split off like enter's:
			// the same bytes, but the Return no longer rides inside the
			// paste, where zsh files it as a newline in the line.
			name:     "text already ends in a newline",
			in:       "hello\n",
			enter:    true,
			wantBody: "hello",
			wantTail: "\r",
		},
		{
			name:     "a trailing newline is a Return without enter too",
			in:       "echo AAA\n",
			enter:    false,
			wantBody: "echo AAA",
			wantTail: "\r",
		},
		{
			name:     "text already ends in a carriage return",
			in:       "echo AAA\r",
			enter:    false,
			wantBody: "echo AAA",
			wantTail: "\r",
		},
		{
			// Only the last Return is split; one before it stays in the text.
			name:     "two trailing newlines",
			in:       "hello\n\n",
			enter:    false,
			wantBody: "hello\r",
			wantTail: "\r",
		},
		{
			// A lone newline is the bare Return that submits what is
			// already in the input box: nothing to split it from.
			name:     "a lone newline stays one bare Return",
			in:       "\n",
			enter:    false,
			wantBody: "\r",
			wantTail: "",
		},
		{
			// Interior newlines still become Enter, as before; only the final
			// Return is separated.
			name:     "multi-line body keeps its interior returns",
			in:       "one\ntwo",
			enter:    true,
			wantBody: "one\rtwo",
			wantTail: "\r",
		},
		{
			name:     "a bare control character is delivered untouched",
			in:       "\x03",
			enter:    false,
			wantBody: "\x03",
			wantTail: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, tail, err := PrepareInjectKeysSplit(tc.in, tc.enter)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(body) != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
			if string(tail) != tc.wantTail {
				t.Errorf("tail = %q, want %q", tail, tc.wantTail)
			}
			// Whatever the split, the bytes that reach the pty must be exactly
			// what the old single-payload path would have sent.
			whole, err := PrepareInjectKeys(tc.in, tc.enter)
			if err != nil {
				t.Fatalf("PrepareInjectKeys: %v", err)
			}
			if got := string(body) + string(tail); got != string(whole) {
				t.Errorf("split bytes %q != unsplit %q", got, whole)
			}
		})
	}
}

// Empty keys with enter is how a caller presses Return on its own — the rescue
// move when text is already sitting in a target's input box. It must stay a
// single bare Return with nothing split off it.
func TestPrepareInjectKeysSplitBareReturn(t *testing.T) {
	body, tail, err := PrepareInjectKeysSplit("", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(body) != "\r" || tail != nil {
		t.Fatalf("body=%q tail=%q, want body=\"\\r\" and no tail", body, tail)
	}
}

// The report (issue 20261116-101644-9FT4RXH2): send_keys "echo AAA\r" in one
// call sat unsubmitted at a zsh prompt. zsh turns bracketed paste on, the
// text was long enough to be wrapped as a paste, and the Return went inside
// the markers. Every Return a send_keys call presses has to reach the program
// outside any paste.
func TestTrailingReturnIsNeverInsideThePaste(t *testing.T) {
	a := &Agent{}
	a.bracketedPaste.Store(true)
	for _, keys := range []string{"echo AAA\r", "echo AAA\n", "/full/path/to/claude --dangerously-skip-permissions\n"} {
		body, tail, err := PrepareInjectKeysSplit(keys, false)
		if err != nil {
			t.Fatalf("%q: %v", keys, err)
		}
		pasted := a.wrapPaste(body)
		if !bytes.HasPrefix(pasted, pasteStart) {
			t.Fatalf("%q: test premise: the text should be wrapped as a paste, got %q", keys, pasted)
		}
		if bytes.HasSuffix(bytes.TrimSuffix(pasted, pasteEnd), []byte{'\r'}) {
			t.Errorf("%q: the Return is inside the paste: %q", keys, pasted)
		}
		if string(a.wrapPaste(tail)) != "\r" {
			t.Errorf("%q: the Return should follow as its own bare key, got tail %q", keys, tail)
		}
	}
}

func TestPrepareInjectKeysSplitRejectsEmpty(t *testing.T) {
	if _, _, err := PrepareInjectKeysSplit("", false); err == nil {
		t.Fatal("empty keys with no Return should be an error")
	}
}
