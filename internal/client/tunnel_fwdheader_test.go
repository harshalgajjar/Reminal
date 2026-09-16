// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "testing"

// TestIsForwardingHeader guards the deny-list of client-supplied forwarding /
// client-IP headers the agent must NOT relay to the backend: a visitor could
// otherwise spoof their source IP to an app that trusts one for an allowlist,
// rate-limit, or audit log. cf-connecting-ip is deliberately allowed through
// (edge-overwritten, authoritative, and the agent's own X-Forwarded-For source).
func TestIsForwardingHeader(t *testing.T) {
	dropped := []string{
		"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
		"X-Forwarded-Port", "X-Forwarded-Scheme", "X-Real-IP", "Forwarded",
		"True-Client-IP", "X-Client-IP", "X-Cluster-Client-IP",
		// case-insensitive
		"x-forwarded-for", "true-client-ip", "X-CLIENT-IP",
	}
	for _, h := range dropped {
		if !isForwardingHeader(h) {
			t.Errorf("isForwardingHeader(%q) = false, want true (must be dropped)", h)
		}
	}

	allowed := []string{
		"cf-connecting-ip", "CF-Connecting-IP", // authoritative, edge-set — must pass through
		"Content-Type", "Accept", "User-Agent", "Cookie", "Authorization",
		"X-Requested-With", "Referer", "Origin",
	}
	for _, h := range allowed {
		if isForwardingHeader(h) {
			t.Errorf("isForwardingHeader(%q) = true, want false (must pass through)", h)
		}
	}
}
