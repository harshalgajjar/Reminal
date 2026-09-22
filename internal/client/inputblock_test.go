// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"strings"
	"testing"
	"time"

	"reminal/internal/crypto"
)

func noticeAgent(t *testing.T) *Agent {
	t.Helper()
	key, _ := crypto.NewSessionKey()
	box, _ := crypto.NewBox(key)
	return &Agent{box: box, buf: newScrollback(1 << 20)}
}

// TestInputBlockedNoticeIsThrottled pins the rate limit. An input event arrives
// per mouse MOVE, so announcing the obstruction every time would bury the
// session in the message — the notice has to be rare enough to read.
func TestInputBlockedNoticeIsThrottled(t *testing.T) {
	a := noticeAgent(t)

	a.noteInputBlocked("System Properties")
	first := a.inputBlockAt
	if a.inputBlockWho != "System Properties" || first.IsZero() {
		t.Fatal("the first obstruction went unrecorded")
	}

	// A drag's worth of events about the same window: still one notice.
	for i := 0; i < 50; i++ {
		a.noteInputBlocked("System Properties")
	}
	if !a.inputBlockAt.Equal(first) {
		t.Error("repeated events re-announced the same obstruction")
	}

	// A DIFFERENT window is news, whenever it happens.
	a.noteInputBlocked("Registry Editor")
	if a.inputBlockWho != "Registry Editor" || a.inputBlockAt.Equal(first) {
		t.Error("a new obstruction was suppressed by the previous one's throttle")
	}

	// And the same one is news again once the window has lapsed.
	a.inputBlockAt = time.Now().Add(-inputBlockNoticeEvery - time.Second)
	stale := a.inputBlockAt
	a.noteInputBlocked("Registry Editor")
	if a.inputBlockAt.Equal(stale) {
		t.Error("the notice never repeats, so a user who missed it never learns why")
	}
}

// Nothing to report must stay silent — the common case is every session that
// never touches an elevated window.
func TestNoNoticeWithoutAnObstruction(t *testing.T) {
	a := noticeAgent(t)
	a.noteInputBlocked("")
	if a.inputBlockWho != "" || !a.inputBlockAt.IsZero() {
		t.Error("recorded an obstruction that was never reported")
	}
	if got := a.buf.LatestSeq(); got != 0 {
		t.Errorf("wrote %d notice(s) into the session with nothing to say", got)
	}
}

// The message has to carry the cause and a way out; "input blocked" alone sends
// the user hunting through reminal for a bug that is not there.
func TestInputBlockedNoticeExplainsItself(t *testing.T) {
	a := noticeAgent(t)
	a.localActive = false // host mirror off; the scrollback copy is what viewers read
	a.noteInputBlocked("System Properties")
	if a.buf.LatestSeq() == 0 {
		t.Fatal("the obstruction was recorded but never announced")
	}
	enc := a.buf.From(0)
	var text strings.Builder
	for _, chunk := range enc {
		pt, err := a.box.Decrypt(chunk.Data)
		if err != nil {
			t.Fatalf("decrypt notice: %v", err)
		}
		text.Write(pt)
	}
	msg := text.String()
	for _, want := range []string{"System Properties", "administrator", "focus"} {
		if !strings.Contains(msg, want) {
			t.Errorf("notice %q omits %q", msg, want)
		}
	}
}
