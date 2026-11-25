// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// crashHelperEnv names the home a helper process is to die in
// (TestAGoroutinesPanicIsKeptInTheCrashFile).
const crashHelperEnv = "REMINAL_TEST_CRASH_HOME"

// A panic in a goroutine of a process whose standard error goes nowhere, as
// a background session's does, is kept in the session's crash file, under
// the line that says which process it was.
func TestAGoroutinesPanicIsKeptInTheCrashFile(t *testing.T) {
	if home := os.Getenv(crashHelperEnv); home != "" {
		// The helper: this test's own binary, started below. TestMain gave
		// it a home of its own, which nothing is left to remove once it
		// dies: it goes first.
		if old := os.Getenv("HOME"); strings.HasPrefix(filepath.Base(old), "reminal-test-home-") {
			_ = os.RemoveAll(old)
		}
		os.Setenv("HOME", home)
		os.Setenv("USERPROFILE", home)
		if err := KeepCrashOutput("crsh1234", "started, v0.0.0-test"); err != nil {
			fmt.Println("KeepCrashOutput:", err)
			os.Exit(3)
		}
		go func() { panic("the helper's own panic") }()
		time.Sleep(10 * time.Second)
		os.Exit(4)
	}
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAGoroutinesPanicIsKeptInTheCrashFile$")
	cmd.Env = append(os.Environ(), crashHelperEnv+"="+home)
	// No Stdout or Stderr: the null device, as a background session's are.
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("the helper ended with %v: want a panic's exit (2)", err)
	}
	p := filepath.Join(home, ".reminal", "restore", "crash-CRSH1234.log")
	b, rerr := os.ReadFile(p)
	if rerr != nil {
		t.Fatal(rerr)
	}
	got := string(b)
	lines := strings.Split(got, "\n")
	if !strings.HasPrefix(lines[0], "# ") || !strings.Contains(lines[0], fmt.Sprintf(" pid %d ", cmd.Process.Pid)) || !strings.HasSuffix(lines[0], ": started, v0.0.0-test") {
		t.Errorf("first line %q: want the process's own line, with its pid and what it was", lines[0])
	}
	if !strings.Contains(got, "panic: the helper's own panic") || !strings.Contains(got, "goroutine ") {
		t.Errorf("crash file %q: want the panic and its goroutine", got)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("crash file %v (%v): want mode 0600", fi, err)
		}
	}
}

// A crash file nothing died into is dropped when its session ends in order;
// one that says something stays. Past its bound it starts again, the last
// one kept, and one no process has written to for a month goes.
func TestCrashFilesStayOnlyWhileTheySaySomethingAndStayBounded(t *testing.T) {
	h := isolateHome(t) // ~/.reminal
	// This test's own process stays as it was once the test is done.
	t.Cleanup(func() { _ = debug.SetCrashOutput(nil, debug.CrashOptions{}) })
	p := filepath.Join(h, "restore", "crash-QUIET123.log")
	if err := KeepCrashOutput("quiet123", "started\x1b[2J, v1"); err != nil {
		t.Fatal(err)
	}
	if err := KeepCrashOutput("quiet123", "restarted in place, v2"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\n"); n != 2 || strings.ContainsRune(string(b), 0x1b) || !strings.Contains(string(b), ": restarted in place, v2\n") {
		t.Errorf("crash file %q: want one line per process, with nothing a terminal would act on", b)
	}
	if CrashLogSaysSomething("quiet123") {
		t.Error("a file of its own lines only says something")
	}
	DropQuietCrashLog("quiet123")
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a crash file nothing died into was kept (%v)", err)
	}

	loud := filepath.Join(h, "restore", "crash-LOUD1234.log")
	if err := os.WriteFile(loud, []byte("# then pid 1 reminal: started, v1\npanic: something\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !CrashLogSaysSomething("loud1234") {
		t.Error("a file with a panic in it says nothing")
	}
	DropQuietCrashLog("loud1234")
	if _, err := os.Stat(loud); err != nil {
		t.Errorf("a crash file that says something was dropped: %v", err)
	}

	if err := os.WriteFile(loud, []byte(strings.Repeat("x", crashLogMax+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := KeepCrashOutput("loud1234", "started, v3"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(loud); err != nil || fi.Size() > 400 {
		t.Errorf("the crash file did not start again past its bound: %v %v", fi, err)
	}
	if _, err := os.Stat(loud + ".1"); err != nil {
		t.Errorf("the last crash file was not kept: %v", err)
	}

	old := filepath.Join(h, "restore", "crash-OLD12345.log")
	other := filepath.Join(h, "restore", "OLD12345.json")
	for _, f := range []string{old, old + ".1", other} {
		if err := os.WriteFile(f, []byte("panic: long ago\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		then := time.Now().Add(-crashLogKeep - time.Hour)
		if err := os.Chtimes(f, then, then); err != nil {
			t.Fatal(err)
		}
	}
	if err := KeepCrashOutput("loud1234", "started, v4"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{old, old + ".1"} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: a crash file a month unwritten was kept (%v)", filepath.Base(f), err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a file that is not a crash file was removed: %v", err)
	}
	if _, err := CrashLogPath("../x"); err == nil {
		t.Error("a crash file path was made from an id that is not one")
	}
}

// A record whose process is gone is removed with a line saying it went
// without a word, once; not when the session said how it ended. A signal
// reminal sends is kept with who sent it and why.
func TestARecordsProcessGoneIsSaidOnceUnlessItSaidItsEnd(t *testing.T) {
	h := isolateHome(t)
	events := func() string { b, _ := os.ReadFile(filepath.Join(h, "restore", "events.log")); return string(b) }
	dead := deadPID(t)
	write := func(id string) {
		t.Helper()
		if err := WriteActive(Active{ID: id, PID: dead, StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	write("GONE1234")
	if err := os.MkdirAll(filepath.Join(h, "restore"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, "restore", "crash-GONE1234.log"), []byte("# x\nfatal error: something\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if all, err := ReadAllActive(); err != nil || len(all) != 0 {
			t.Fatalf("ReadAllActive: %v %v", all, err)
		}
	}
	got := events()
	want := fmt.Sprintf(" GONE1234 ended without a word: pid %d is gone, its record removed by ", dead)
	if strings.Count(got, want) != 1 || !strings.Contains(got, "; its last words are in crash-GONE1234.log\n") {
		t.Errorf("events %q: want once %q, pointing at its crash file", got, want)
	}

	LogEnded("said1234", "its shell ended: exit status 0 (pid %d)", dead)
	write("SAID1234")
	if _, err := ReadActiveByID("SAID1234"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a record with a dead pid was read: %v", err)
	}
	if got := events(); strings.Contains(got, " SAID1234 ended without a word") || !strings.Contains(got, " SAID1234 ended: its shell ended: exit status 0 (pid ") {
		t.Errorf("events %q: a session that said its end is said to have gone without a word", got)
	}

	LogSent("sent1234", 4242, "terminate", "it did not end when asked to quit")
	if got := events(); !strings.Contains(got, fmt.Sprintf(" SENT1234 terminate sent to pid 4242 by %s (pid %d): it did not end when asked to quit\n", senderName(), os.Getpid())) {
		t.Errorf("events %q: want the signal, who sent it and why", got)
	}
	if lastLogged("sent1234") == "" || lastLogged("none1234") != "" {
		t.Errorf("last lines: %q, %q", lastLogged("sent1234"), lastLogged("none1234"))
	}
}

// deadPID is the pid of a process that has ended: this test binary, run
// with no test to run, and waited for.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// A crash file nothing died into does not outlive its session's process:
// whoever finds the process gone removes it, whether the session had said
// its end or not, and leaves one that says something.
func TestAQuietCrashFileGoesWhenItsProcessIsFoundGone(t *testing.T) {
	h := isolateHome(t)
	dead := deadPID(t)
	if err := os.MkdirAll(filepath.Join(h, "restore"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := func(id string) string { return filepath.Join(h, "restore", "crash-"+id+".log") }
	for _, id := range []string{"QUIETGON", "SAIDGONE", "LOUDGONE"} {
		body := "# then pid 1 reminal: started, v1\n"
		if id == "LOUDGONE" {
			body += "panic: something\n"
		}
		if err := os.WriteFile(file(id), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if id == "SAIDGONE" {
			LogEnded(id, "quit: asked to stop (pid %d)", dead)
		}
		if err := WriteActive(Active{ID: id, PID: dead, StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if all, err := ReadAllActive(); err != nil || len(all) != 0 {
		t.Fatalf("ReadAllActive: %v %v", all, err)
	}
	for _, id := range []string{"QUIETGON", "SAIDGONE"} {
		if _, err := os.Stat(file(id)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: a crash file nothing died into was left after its process was found gone (%v)", id, err)
		}
	}
	if _, err := os.Stat(file("LOUDGONE")); err != nil {
		t.Errorf("a crash file that says something was removed: %v", err)
	}
}
