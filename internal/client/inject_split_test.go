// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "testing"

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
			// The caller's own trailing newline is their bytes. Splitting it off
			// would send something different from what they wrote.
			name:     "text already ends in a newline",
			in:       "hello\n",
			enter:    true,
			wantBody: "hello\r",
			wantTail: "",
		},
		{
			name:     "text already ends in a carriage return",
			in:       "hello\r",
			enter:    true,
			wantBody: "hello\r",
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

func TestPrepareInjectKeysSplitRejectsEmpty(t *testing.T) {
	if _, _, err := PrepareInjectKeysSplit("", false); err == nil {
		t.Fatal("empty keys with no Return should be an error")
	}
}
