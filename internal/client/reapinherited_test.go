//go:build !windows

package client

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestReapInheritedClearsDeadAndSparesLiving is the safety property that makes
// the startup sweep acceptable: it must collect children that already exited
// (the backlog a hot restart inherits) and must NOT wait on the live PTY shell
// the restarted agent just took over.
func TestReapInheritedClearsDeadAndSparesLiving(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()

	// A living child, standing in for the inherited shell.
	live := exec.Command("/bin/sh", "-c", "sleep 30")
	live.Stdin, live.Stdout, live.Stderr = devnull, devnull, devnull
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = live.Process.Kill()
		_ = live.Wait()
	}()

	// Dead children nobody waited on — exactly what Release() used to leave.
	const dead = 10
	for i := 0; i < dead; i++ {
		c := exec.Command("/bin/sh", "-c", "exit 0")
		c.Stdin, c.Stdout, c.Stderr = devnull, devnull, devnull
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		_ = c.Process.Release()
	}

	// Let them all exit before sweeping.
	// They exit on their own schedule, so a single sweep can catch only the
	// ones that have gone by then; accumulate across sweeps.
	deadline := time.Now().Add(5 * time.Second)
	got := 0
	for got < dead && time.Now().Before(deadline) {
		got += reapInherited()
		time.Sleep(50 * time.Millisecond)
	}
	if got < dead {
		t.Fatalf("reaped %d of %d dead children", got, dead)
	}

	// The live child must still be running and still ours: if the sweep had
	// blocked on it, we would never have got here, and if it had somehow
	// collected it, this signal would fail.
	if err := live.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("live child was disturbed by the sweep: %v", err)
	}

	// And a second sweep on a clean process reports nothing to do.
	if n := reapInherited(); n != 0 {
		t.Fatalf("second sweep reaped %d, want 0", n)
	}
}
