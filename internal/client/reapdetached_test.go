//go:build !windows

package client

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestReapDetachedLeavesNoZombie is the regression test for the leak that
// Release() hid: a parent that outlives the children it spawns. Release keeps
// the process table entry until someone Waits, so this fails against
// cmd.Process.Release() and passes with reapDetached.
func TestReapDetachedLeavesNoZombie(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()

	const children = 12
	pids := make([]int, 0, children)
	for i := 0; i < children; i++ {
		cmd := exec.Command("/bin/sh", "-c", "exit 0")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pids = append(pids, cmd.Process.Pid)
		reapDetached(cmd)
	}

	// The children exit immediately; give the reaper goroutines a moment, then
	// assert none of them is still sitting in the table as ours.
	deadline := time.Now().Add(5 * time.Second)
	for {
		left := zombieChildren(t, pids)
		if left == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d detached children are still defunct — nothing reaped them", left, children)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func zombieChildren(t *testing.T, pids []int) int {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid,ppid,stat").Output()
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	want := make(map[int]bool, len(pids))
	for _, p := range pids {
		want[p] = true
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil || !want[pid] {
			continue
		}
		if ppid, _ := strconv.Atoi(f[1]); ppid == os.Getpid() && strings.HasPrefix(f[2], "Z") {
			n++
		}
	}
	return n
}
