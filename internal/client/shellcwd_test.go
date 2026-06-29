package client

import (
	"os"
	"path/filepath"
	"testing"
)

// TestShellCwdSelf checks the live-cwd reader against this test process —
// exercises the real /proc (Linux) or lsof (macOS) path on whatever platform
// the tests run on.
func TestShellCwdSelf(t *testing.T) {
	got := shellCwd(os.Getpid())
	if got == "" {
		t.Skip("shellCwd unsupported or unavailable on this platform/env")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Resolve symlinks on both sides (macOS reports /private/var for /var, etc).
	wantR, _ := filepath.EvalSymlinks(wd)
	gotR, _ := filepath.EvalSymlinks(got)
	if gotR != wantR {
		t.Fatalf("shellCwd(self) = %q (resolved %q), want %q", got, gotR, wantR)
	}
}

func TestShellCwdInvalidPID(t *testing.T) {
	if got := shellCwd(0); got != "" {
		t.Errorf("shellCwd(0) = %q, want empty", got)
	}
	if got := shellCwd(-1); got != "" {
		t.Errorf("shellCwd(-1) = %q, want empty", got)
	}
}
