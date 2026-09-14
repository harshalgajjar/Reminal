// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"strings"
	"testing"
)

func TestSchemaStaleWarning(t *testing.T) {
	origVersion := version
	origOnDisk := onDiskReminalVersion
	t.Cleanup(func() {
		version = origVersion
		onDiskReminalVersion = origOnDisk
	})
	// Keep the on-disk probe from execing a real binary in any subtest that
	// doesn't set it; each case overrides as needed.
	onDiskReminalVersion = func() string { return "" }

	t.Run("dev build never warns", func(t *testing.T) {
		version = "dev"
		t.Setenv(bootVersionEnv, "3.13.2")
		onDiskReminalVersion = func() string { return "3.99.0" }
		if w := schemaStaleWarning(); w != "" {
			t.Fatalf("dev build must not warn, got %q", w)
		}
	})

	t.Run("re-exec onto newer build warns and says params work", func(t *testing.T) {
		version = "3.13.5"
		t.Setenv(bootVersionEnv, "3.13.2")
		w := schemaStaleWarning()
		if !strings.Contains(w, "3.13.5") || !strings.Contains(w, "3.13.2") {
			t.Fatalf("want both versions in warning, got %q", w)
		}
		if !strings.Contains(w, "already accepts") {
			t.Fatalf("re-exec warning should say the running server honors new params, got %q", w)
		}
	})

	t.Run("on-disk newer warns and says restart", func(t *testing.T) {
		version = "3.13.2"
		t.Setenv(bootVersionEnv, "3.13.2") // boot == running, so Case 1 is skipped
		onDiskReminalVersion = func() string { return "3.13.7" }
		w := schemaStaleWarning()
		if !strings.Contains(w, "3.13.7") || !strings.Contains(w, "3.13.2") {
			t.Fatalf("want both versions in warning, got %q", w)
		}
		if !strings.Contains(w, "Restart") {
			t.Fatalf("on-disk-newer warning should tell the client to restart, got %q", w)
		}
	})

	t.Run("no skew is silent", func(t *testing.T) {
		version = "3.13.2"
		t.Setenv(bootVersionEnv, "3.13.2")
		onDiskReminalVersion = func() string { return "3.13.2" }
		if w := schemaStaleWarning(); w != "" {
			t.Fatalf("matching versions must not warn, got %q", w)
		}
	})

	t.Run("older on-disk (downgrade) is silent", func(t *testing.T) {
		version = "3.13.4"
		t.Setenv(bootVersionEnv, "3.13.4")
		onDiskReminalVersion = func() string { return "3.13.1" }
		if w := schemaStaleWarning(); w != "" {
			t.Fatalf("an older on-disk binary is not skew, got %q", w)
		}
	})
}
