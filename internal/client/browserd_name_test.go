// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "testing"

// The bridge is taken only from a browser of Chrome's family, told by its
// program's name. Chrome for Testing is one of them: it was turned away, so
// the browser tools did not work in it.
func TestBrowserProgramNames(t *testing.T) {
	for name, want := range map[string]bool{
		"Google Chrome":                               true,
		"Google Chrome Helper":                        true,
		"Google Chrome Helper (Renderer)":             true,
		"Google Chrome Beta":                          true,
		"Google Chrome for Testing":                   true,
		"Google Chrome for Testing Helper":            true,
		"Google Chrome for Testing Helper (Renderer)": true,
		"chrome":                            true,
		"Chromium":                          true,
		"chromium-browser":                  true,
		"msedge":                            true,
		"Microsoft Edge":                    true,
		"Brave Browser":                     true,
		"Arc":                               true,
		"headless_shell":                    true,
		"Google Chrome for Testing Updater": false,
		"Google Chrome for Something":       false,
		"Not Google Chrome":                 false,
		"chrome-wrapper":                    false,
		"firefox":                           false,
		"node":                              false,
		"":                                  false,
	} {
		if got := browserProcessName.MatchString(name); got != want {
			t.Errorf("program %q: taken as a browser %v, want %v", name, got, want)
		}
	}
}
