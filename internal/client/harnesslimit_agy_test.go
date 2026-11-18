// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "testing"

// Antigravity CLI 1.5.11 stops a turn at once on a used-up daily quota, a
// spend cap or prepaid credits gone: its error row, led by its warning
// sign, with the service's 429 and RESOURCE_EXHAUSTED in it, wrapped over
// the rows below, then its prompt. Recorded in Docker, its service answered
// by a stand-in, 2026-11-18.
const agyFrame = "\n────────────────────────────────────────\n>\n────────────────────────────────────────\n? for shortcuts                                      Gemini 3.1 · low"

func TestAgyQuotaStopIsALimit(t *testing.T) {
	for _, s := range []string{
		"> say hi (day)\n⚠ agent executor error: generating and executing: Error 429, Message: You exceeded your current quota, please check\nyour plan and billing details., Status: RESOURCE_EXHAUSTED, Details:\n[map[@type:type.googleapis.com/google.rpc.QuotaFailure violations:[map[quotaId:GenerateRequestsPerDayPerProjectPerModel\nquotaMetric:generativelanguage.googleapis.com/generate_content_paid_tier_input_token_count]]]\nmap[@type:type.googleapis.com/google.rpc.RetryInfo retryDelay:3600s]]\nError ID: 4ffa14b5-6bbd-4663-bdfd-98a5d6bca9da-7" + agyFrame,
		"> say hi (cap)\n⚠ agent executor error: generating and executing: Error 429, Message: Your project has exceeded its monthly spending\ncap. Please go to AI Studio at https://ai.studio/spend to manage your project spend cap., Status: RESOURCE_EXHAUSTED,\nDetails: [map[@type:type.googleapis.com/google.rpc.ErrorInfo reason:SPEND_CAP_EXCEEDED]]\nError ID: 0b1c2d3e-0000-0000-0000-000000000000-1" + agyFrame,
		"> say hi (prepaid)\n⚠ agent executor error: generating and executing: Error 429, Message: Your prepayment credits are depleted. Please go\nto AI Studio at https://ai.studio/projects to manage your project and billing., Status: RESOURCE_EXHAUSTED, Details: []\nError ID: 0b1c2d3e-0000-0000-0000-000000000000-2" + agyFrame,
		// On a narrow screen the status wraps further down.
		"> hi\n⚠ agent executor error: generating and executing: Error 429,\nMessage: You exceeded your current quota, please\ncheck your plan and billing details., Status:\nRESOURCE_EXHAUSTED, Details: []" + agyFrame,
	} {
		if hit, _ := harnessLimitIn(s, "agy", limitTestNow); !hit {
			t.Errorf("its quota stop was not read as a limit:\n%s", s)
		}
	}
}

// Its per-minute limit is waited out, the turn going on; another error, an
// answer about the error, or the row under another agent is not a limit.
func TestAgyQuotaStopIsOnlyItsOwnRow(t *testing.T) {
	for _, c := range []struct{ prog, screen, why string }{
		{"agy", "> say hi\n⣻  Model unavailable (attempt 1/9), retrying in 33s...\n\nesc to cancel                                      Gemini 3.1 · low", "waited out, the turn goes on"},
		{"agy", "> say hi\n⚠ agent executor error: generating and executing: Error 500, Message: Internal error encountered., Status: INTERNAL, Details: []" + agyFrame, "another error"},
		{"agy", "> say hi\n⚠ agent executor error: generating and executing: Error 400, Message: API key not valid. Status: RESOURCE_EXHAUSTED was not it." + agyFrame, "another code"},
		{"agy", "> why did it stop?\n● It printed ⚠ agent executor error: generating and executing: Error 429, Message: quota, Status: RESOURCE_EXHAUSTED." + agyFrame, "an answer quoting it"},
		{"agy", "> why did it stop?\n● ⚠ agent executor error: generating and executing: Error 429, Message: You exceeded your current quota., Status: RESOURCE_EXHAUSTED, Details: []" + agyFrame, "an answer quoting it at its start"},
		{"agy", "> why?\n  \"⚠ agent executor error: generating and executing: Error 429, Message: quota, Status: RESOURCE_EXHAUSTED\"" + agyFrame, "a quoted string"},
		{"claude", "⚠ agent executor error: generating and executing: Error 429, Message: You exceeded your current quota, please check\nyour plan and billing details., Status: RESOURCE_EXHAUSTED, Details: []\n❯ ", "under another agent"},
	} {
		if hit, _ := harnessLimitIn(c.screen, c.prog, limitTestNow); hit {
			t.Errorf("%s (%s): read as at its limit:\n%s", c.prog, c.why, c.screen)
		}
	}
}
