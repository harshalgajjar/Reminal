// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"testing"
)

func TestPrepareInjectKeysEnterAndNewline(t *testing.T) {
	got, err := PrepareInjectKeys("ls\n", false)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ls\r" {
		t.Fatalf("newline: %q", got)
	}
	got, err = PrepareInjectKeys("ls", true)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ls\r" {
		t.Fatalf("enter: %q", got)
	}
	if _, err := PrepareInjectKeys("", false); err == nil {
		t.Fatal("empty keys should fail")
	}
	got, err = PrepareInjectKeys("", true)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\r" {
		t.Fatalf("bare enter: %q", got)
	}
}

func TestSendKeysPINRejectsBadPIN(t *testing.T) {
	if err := SendKeysPIN("ABCDEFGH", "12", []byte("x")); err == nil {
		t.Fatal("expected PIN validation error")
	}
}

func TestKeysControlRejectsNoPTY(t *testing.T) {
	a := &Agent{}
	stop := a.listenControl()
	defer stop()
	data, err := PrepareInjectKeys("x", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := InjectAgentKeys(os.Getpid(), data); err == nil {
		t.Fatal("expected no pty error")
	}
}

func TestBracketedPasteSniffAndWrap(t *testing.T) {
	a := &Agent{}
	// Nothing seen yet: injected text goes through untouched, because a program
	// that never asked for paste markers would render them as garbage.
	if got := a.wrapPaste([]byte("hello")); string(got) != "hello" {
		t.Fatalf("wrapped before the mode was seen: %q", got)
	}
	a.sniffBracketedPaste([]byte("\x1b[?2004h"))
	got := string(a.wrapPaste([]byte("hello, this is a message")))
	if got != "\x1b[200~hello, this is a message\x1b[201~" {
		t.Fatalf("wrapPaste = %q, want the text between paste markers", got)
	}
	// A one-letter menu answer is a KEYPRESS. A single-key menu ignores pasted
	// text, so wrapping "y" would re-break the approval path.
	for _, short := range []string{"y", "1", "yes"} {
		if string(a.wrapPaste([]byte(short))) != short {
			t.Fatalf("a short answer %q was wrapped as a paste", short)
		}
	}
	// A keystroke is not a paste. Wrapping a bare Enter would submit the
	// markers instead of pressing the key.
	for _, k := range []string{"\r", "\x03", "\x1b"} {
		if string(a.wrapPaste([]byte(k))) != k {
			t.Fatalf("a control keystroke %q was wrapped as pasted text", k)
		}
	}
	a.sniffBracketedPaste([]byte("\x1b[?2004l"))
	if got := a.wrapPaste([]byte("hello")); string(got) != "hello" {
		t.Fatalf("still wrapping after the program turned the mode off: %q", got)
	}
}

func TestBracketedPasteSplitAcrossReads(t *testing.T) {
	a := &Agent{}
	// The sequence arriving in two PTY reads must still be seen — it is emitted
	// once at startup, so missing it means every injection for the life of the
	// session is wrong.
	a.sniffBracketedPaste([]byte("some output\x1b[?20"))
	a.sniffBracketedPaste([]byte("04h more"))
	if string(a.wrapPaste([]byte("a long enough line"))) != "\x1b[200~a long enough line\x1b[201~" {
		t.Fatal("a mode sequence split across two reads was missed")
	}
}
