// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/reminal/reminal/internal/crypto"
)

func TestStripANSIDropsPaintKeepsText(t *testing.T) {
	in := "\x1b[32mhello\x1b[0m\r\n\x1b]0;title\x07world"
	got := stripANSI(in)
	if got != "hello\nworld" {
		t.Fatalf("stripANSI = %q", got)
	}
}

func TestTranscriptHitsSnippetAndCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString("xxxx needle yyyy\n")
	}
	re := regexp.MustCompile("needle")
	hits := transcriptHits(b.String(), re)
	if len(hits) != maxTranscriptHits {
		t.Fatalf("got %d hits, want cap %d", len(hits), maxTranscriptHits)
	}
	if !strings.Contains(hits[0], "needle") {
		t.Fatalf("snippet missing match: %q", hits[0])
	}
	if strings.Contains(hits[0], "\n") {
		t.Fatalf("snippet should be one line: %q", hits[0])
	}
}

func TestPlaintextTranscriptDecryptsAndStrips(t *testing.T) {
	key := make([]byte, 32)
	box, err := crypto.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := box.Encrypt([]byte("\x1b[1malpha\x1b[0m beta"))
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{box: box, buf: newScrollback(1 << 20)}
	a.buf.Append(enc)
	if got := a.plaintextTranscript(); got != "alpha beta" {
		t.Fatalf("plaintext = %q", got)
	}
}

func TestSearchControlFindsScrollback(t *testing.T) {
	// Socket lives under ~/.reminal (same as production). isolateHome's
	// temp path exceeds macOS AF_UNIX sun_path, so we stay on the real home.
	key := make([]byte, 32)
	box, err := crypto.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := box.Encrypt([]byte("ready Get-Date done"))
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{box: box, buf: newScrollback(1 << 20)}
	a.buf.Append(enc)
	stop := a.listenControl()
	defer stop()

	hits, err := SearchAgentTranscript(os.Getpid(), "Get-Date")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0], "Get-Date") {
		t.Fatalf("hits = %#v", hits)
	}

	if _, err := SearchAgentTranscript(os.Getpid(), "("); err == nil {
		t.Fatal("invalid regex should fail")
	}
}

func TestDirQueryPatternDecryptsOrIgnores(t *testing.T) {
	key := make([]byte, 32)
	box, err := crypto.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	if got := parseDirQuery(box, ""); got.Pattern != "" || got.Transcript != "" {
		t.Fatalf("empty data: %+v", got)
	}
	if got := parseDirQuery(box, "not-ciphertext"); got.Pattern != "" {
		t.Fatalf("garbage: %+v", got)
	}
	enc, err := box.Encrypt([]byte(`{"pattern":"  foo  ","transcript":" ABC "}`))
	if err != nil {
		t.Fatal(err)
	}
	got := parseDirQuery(box, enc)
	if got.Pattern != "foo" || got.Transcript != "ABC" {
		t.Fatalf("req = %+v", got)
	}
}

func TestClipTranscriptKeepsTail(t *testing.T) {
	got, trunc := clipTranscript("abcdefghij", 4)
	if !trunc || got != "ghij" {
		t.Fatalf("clip = %q trunc=%v", got, trunc)
	}
	got, trunc = clipTranscript("short", 100)
	if trunc || got != "short" {
		t.Fatalf("short clip = %q trunc=%v", got, trunc)
	}
}

func TestTranscriptControlDumpsPlaintext(t *testing.T) {
	key := make([]byte, 32)
	box, err := crypto.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := box.Encrypt([]byte("\x1b[32mhello world\x1b[0m"))
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{box: box, buf: newScrollback(1 << 20)}
	a.buf.Append(enc)
	stop := a.listenControl()
	defer stop()

	text, truncated, err := ReadAgentTranscript(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("short dump should not truncate")
	}
	if text != "hello world" {
		t.Fatalf("text = %q", text)
	}
}
