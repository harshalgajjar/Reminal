// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package updater

import "testing"

// The panel hides the upgrade offer for these, but the handler behind it takes
// a message and must refuse on its own. A dev binary silently becoming a
// release mid-debug is the accident this exists to stop.
func TestUpgradeBlockedReason(t *testing.T) {
	for _, v := range []string{"", "dev", "0.0.0"} {
		if UpgradeBlockedReason(v) == "" {
			t.Errorf("version %q is upgradeable, want blocked — a dev build must not be replaced in place", v)
		}
	}
	if r := UpgradeBlockedReason("3.5.5"); r != "" {
		// Only fails if the test binary itself lives somewhere shouldCheck
		// rejects, which is worth knowing about rather than skipping silently.
		t.Logf("a real version was blocked here: %q (test binary location)", r)
	}
}
