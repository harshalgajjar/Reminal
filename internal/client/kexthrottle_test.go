// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"testing"
	"time"
)

// TestKexThrottle verifies the agent's kex token bucket: it allows an initial
// burst (so several viewers / a few PIN fat-fingers connect fine) then rate-
// limits, and refills over time. Each answered kex_init is one online PIN
// guess, so this is what bounds a malicious relay's brute-force.
func TestKexThrottle(t *testing.T) {
	a := &Agent{}
	base := time.Unix(1_700_000_000, 0)

	// Burst: the first kexBurst attempts at t0 all pass.
	for i := 0; i < kexBurst; i++ {
		if !a.allowKex(base) {
			t.Fatalf("attempt %d within burst should be allowed", i)
		}
	}
	// The next one (still t0) is throttled — bucket drained.
	if a.allowKex(base) {
		t.Fatalf("attempt past the burst at the same instant should be throttled")
	}

	// After one refill interval, exactly one more token is available.
	if !a.allowKex(base.Add(kexRefill)) {
		t.Fatalf("one token should have refilled after kexRefill")
	}
	if a.allowKex(base.Add(kexRefill)) {
		t.Fatalf("only one token should refill per interval")
	}

	// The bucket never exceeds kexBurst even after a long idle stretch.
	far := base.Add(1000 * kexRefill)
	for i := 0; i < kexBurst; i++ {
		if !a.allowKex(far) {
			t.Fatalf("post-idle burst attempt %d should be allowed", i)
		}
	}
	if a.allowKex(far) {
		t.Fatalf("bucket should be capped at kexBurst even after long idle")
	}
}
