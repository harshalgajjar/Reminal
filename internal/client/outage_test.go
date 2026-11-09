package client

import (
	"testing"
	"time"
)

// A link that a middlebox resets every few seconds, each time coming straight
// back, is one outage: said once, and its end once, after a connection holds.
func TestOutageNoticeFlappingLinkSaysOnce(t *testing.T) {
	var o outageNotice
	o.up()
	t0 := time.Unix(1_000_000, 0)
	said := 0
	// Twenty drops 3s apart, each followed by a connection that held for
	// less than stableThresh (so steady is never called between them).
	for i := 0; i < 20; i++ {
		if o.failed(t0.Add(time.Duration(i)*3*time.Second), false) {
			said++
		}
	}
	if said != 1 {
		t.Fatalf("outage said %d times over a flapping minute, want 1", said)
	}
	if !o.steady() {
		t.Fatal("a connection that held after a said outage should say back online")
	}
	if o.steady() {
		t.Fatal("back online said twice")
	}
}

// A drop that comes straight back is not worth a line at all.
func TestOutageNoticeBlipIsQuiet(t *testing.T) {
	var o outageNotice
	o.up()
	t0 := time.Unix(1_000_000, 0)
	for round := 0; round < 5; round++ {
		at := t0.Add(time.Duration(round) * time.Hour)
		if o.failed(at, false) || o.failed(at.Add(2*time.Second), false) {
			t.Fatalf("round %d: a blip was said", round)
		}
		if o.steady() {
			t.Fatalf("round %d: back online said for an outage never said", round)
		}
	}
}

// The grace runs from when the link stopped being steady, so a long outage is
// said once it passes, and a fresh outage after a steady spell starts anew.
func TestOutageNoticeLongOutage(t *testing.T) {
	var o outageNotice
	o.up()
	t0 := time.Unix(1_000_000, 0)
	if o.failed(t0, false) {
		t.Fatal("said at the first failure")
	}
	if o.failed(t0.Add(outageGrace-time.Second), false) {
		t.Fatal("said inside the grace")
	}
	if !o.failed(t0.Add(outageGrace), false) {
		t.Fatal("not said once the grace passed")
	}
	o.steady()
	t1 := t0.Add(time.Hour)
	if o.failed(t1, false) {
		t.Fatal("a new outage was said at once: the grace should start again")
	}
}

// A throttle's long wait is said at once.
func TestOutageNoticeNowSaysAtOnce(t *testing.T) {
	var o outageNotice
	t0 := time.Unix(1_000_000, 0)
	if !o.failed(t0, true) {
		t.Fatal("a forced outage was not said")
	}
	if o.failed(t0.Add(time.Minute), true) {
		t.Fatal("a forced outage was said twice")
	}
}

// A session started while the relay is out of reach says so at once: there
// is no link to have blipped, and its join link would otherwise sit there
// unexplained.
func TestOutageNoticeNeverReachedSaysAtOnce(t *testing.T) {
	var o outageNotice
	t0 := time.Unix(1_000_000, 0)
	if !o.failed(t0, false) {
		t.Fatal("a session that never reached the relay did not say so at once")
	}
	if o.failed(t0.Add(time.Second), false) {
		t.Fatal("said twice")
	}
	o.up()
	if !o.steady() {
		t.Fatal("back online not said once it reached the relay and held")
	}
	if o.failed(t0.Add(time.Hour), false) {
		t.Fatal("a drop after the relay was reached was said at once: it should get the grace")
	}
}
