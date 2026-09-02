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
