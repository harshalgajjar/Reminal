// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// snapHome is a home folder with a snap browser's own folders in it, as the
// snap makes them, short enough for a socket's path.
func snapHome(t *testing.T) (home, exe string) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := os.MkdirAll(filepath.Join(home, "snap", "chromium", "common"), 0o775); err != nil {
		t.Fatal(err)
	}
	exe = filepath.Join(home, "bin-reminal")
	if err := os.WriteFile(exe, []byte("reminal one"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home, exe
}

// A setup puts reminal's copy in the snap's folder, in a folder of the
// person's alone; run again after an upgrade it brings the copy up to date;
// taking it away leaves nothing.
func TestSnapBrowserSetUpAndRemove(t *testing.T) {
	home, exe := snapHome(t)
	if there, _ := SnapBrowserSetUpDone(home, exe); there {
		t.Fatal("a setup is said to be there before any was made")
	}
	if err := SnapBrowserSetUp(home, exe); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(SnapBrowserDir(home))
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the folder: %v, mode %v, want 0700", err, st.Mode().Perm())
	}
	if st, err := os.Stat(SnapBrowserHelper(home)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the copy: %v", err)
	}
	if there, current := SnapBrowserSetUpDone(home, exe); !there || !current {
		t.Fatalf("after a setup: there %v, current %v", there, current)
	}
	// An upgrade: another reminal at exe.
	if err := os.WriteFile(exe, []byte("reminal two!"), 0o755); err != nil {
		t.Fatal(err)
	}
	if there, current := SnapBrowserSetUpDone(home, exe); !there || current {
		t.Fatalf("after an upgrade: there %v, current %v (want there, not current)", there, current)
	}
	if err := SnapBrowserSetUp(home, exe); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(SnapBrowserHelper(home)); string(got) != "reminal two!" {
		t.Errorf("the copy after an upgrade: %q", got)
	}
	// A folder open to others is closed to them.
	if err := os.Chmod(SnapBrowserDir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SnapBrowserSetUp(home, exe); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(SnapBrowserDir(home)); st.Mode().Perm() != 0o700 {
		t.Errorf("the folder was left open to others: %v", st.Mode().Perm())
	}
	if err := SnapBrowserRemove(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(SnapBrowserDir(home)); !os.IsNotExist(err) {
		t.Errorf("the folder is still there after a remove: %v", err)
	}
	if err := SnapBrowserRemove(home); err != nil {
		t.Errorf("a second remove: %v", err)
	}
}

// The confined browser can write in the snap's folders. A folder on the way
// that it turned into a link to somewhere else is never written through:
// not by a setup, not by the hub's socket, not by a manifest.
func TestSnapBrowserNeverFollowsALink(t *testing.T) {
	for _, linked := range []string{"common", "reminal"} {
		home, exe := snapHome(t)
		elsewhere := filepath.Join(home, "elsewhere")
		if err := os.MkdirAll(elsewhere, 0o700); err != nil {
			t.Fatal(err)
		}
		at := filepath.Join(home, "snap", "chromium", "common")
		if linked == "common" {
			if err := os.Remove(at); err != nil {
				t.Fatal(err)
			}
		} else {
			at = filepath.Join(at, "reminal")
		}
		if err := os.Symlink(elsewhere, at); err != nil {
			t.Fatal(err)
		}
		if SnapBrowserCanSetUp(home) {
			t.Errorf("%s a link: said it can be set up", linked)
		}
		if err := SnapBrowserSetUp(home, exe); err == nil {
			t.Errorf("%s a link: the setup went through it", linked)
		}
		// A copy put behind the link by hand: the hub still opens nothing.
		if err := os.WriteFile(filepath.Join(elsewhere, "reminal"), []byte("x"), 0o700); err != nil {
			t.Fatal(err)
		}
		if ln, dir, err := snapBrowserListen(home); err == nil {
			ln.Close()
			dir.Close()
			t.Errorf("%s a link: the hub opened a socket through it", linked)
		}
		if linked == "common" {
			if err := SnapBrowserWriteManifest(home, "m.json", []byte("{}")); err == nil {
				t.Errorf("a manifest was written through the link")
			}
		}
		left, _ := os.ReadDir(elsewhere)
		for _, e := range left {
			if e.Name() != "reminal" {
				t.Errorf("%s a link: %s was put where the link leads", linked, e.Name())
			}
		}
		if got, _ := os.ReadFile(filepath.Join(elsewhere, "reminal")); string(got) != "x" {
			t.Errorf("%s a link: the file behind it was written over", linked)
		}
	}
}

// A folder on the way that is another user's is not used.
func TestSnapBrowserWantsThePersonsOwnFolders(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs to hand a folder to another user")
	}
	home, exe := snapHome(t)
	if err := os.Chown(filepath.Join(home, "snap", "chromium"), 12345, 12345); err != nil {
		t.Fatal(err)
	}
	if SnapBrowserCanSetUp(home) || SnapBrowserSetUp(home, exe) == nil {
		t.Error("a folder of another user's on the way was used")
	}
}

// The hub's second socket: not there until a setup is, open to the person
// alone, served as the first is (a state is answered; a program that is not
// reminal's helper under a browser is not taken in as one), and gone when
// the setup is taken away.
func TestBrowserHubServesASnapBrowsersSocket(t *testing.T) {
	home, exe := snapHome(t)
	t.Setenv("HOME", home)
	t.Setenv("REMINAL_BROWSER_SOCK", "")
	h := newBrowserHub()
	h.noWait = true
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go serveBrowserOn(h, stop)
	main := filepath.Join(home, ".reminal", "browser.sock")
	for i := 0; i < 200; i++ {
		if c, err := net.Dial("unix", main); err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sock := filepath.Join(SnapBrowserDir(home), "browser.sock")
	ask := func(path, line string) string {
		t.Helper()
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatalf("dial %s: %v", path, err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
		got, _ := bufio.NewReader(c).ReadString('\n')
		return got
	}
	ask(main, `{"t":"state"}`)
	if _, err := os.Lstat(SnapBrowserDir(home)); !os.IsNotExist(err) {
		t.Fatalf("with no setup the hub made the snap's folder: %v", err)
	}
	if err := SnapBrowserSetUp(home, exe); err != nil {
		t.Fatal(err)
	}
	// A setup asks the hub its state when it is done: the socket opens then.
	ask(main, `{"t":"state"}`)
	st, err := os.Lstat(sock)
	if err != nil || st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0o600 {
		t.Fatalf("the snap's socket after a setup: %v, %v (want a socket, 0600)", err, st)
	}
	if got := ask(sock, `{"t":"state"}`); !strings.Contains(got, "connected") {
		t.Errorf("a state asked on the snap's socket: %q", got)
	}
	// This test is no helper under a browser: as on the first socket, it is
	// not taken in as the bridge.
	ask(sock, `{"t":"host"}`)
	h.mu.Lock()
	taken := h.host != nil
	h.mu.Unlock()
	if taken {
		t.Error("a program that is not reminal's helper was taken in as the bridge on the snap's socket")
	}
	// A session's request there is checked as on the first: this process is
	// no session.
	if got := ask(sock, `{"t":"req","session":"ABCD1234","tool":"browser_tabs_context"}`); !strings.Contains(got, "itself may ask") {
		t.Errorf("a request from a process that is not the session, on the snap's socket: %q", got)
	}
	// The copy is kept the daemon's own reminal: not checked here (the test
	// binary is large); the setup's own test covers the write.
	if err := SnapBrowserRemove(home); err != nil {
		t.Fatal(err)
	}
	ask(main, `{"t":"state"}`)
	if _, err := net.Dial("unix", sock); err == nil {
		t.Error("the snap's socket still answers after the setup was taken away")
	}
}

// Inside the snap, the helper reaches the hub on the snap's own socket and
// finds the person's real home folder; outside it, as always.
func TestTheHelperInsideASnapUsesItsSocket(t *testing.T) {
	t.Setenv("REMINAL_BROWSER_SOCK", "")
	t.Setenv("HOME", "/home/someone/snap/chromium/3606")
	t.Setenv("SNAP_NAME", "chromium")
	t.Setenv("SNAP_USER_COMMON", "/home/someone/snap/chromium/common")
	t.Setenv("SNAP_REAL_HOME", "/home/someone")
	if got, _ := BrowserHostSockPath(); got != "/home/someone/snap/chromium/common/reminal/browser.sock" {
		t.Errorf("the helper's socket inside the snap: %s", got)
	}
	if got, _ := BrowserHostHome(); got != "/home/someone" {
		t.Errorf("the helper's home inside the snap: %s", got)
	}
	// A session started from a program of another snap is not the helper.
	t.Setenv("SNAP_NAME", "code")
	if got, _ := BrowserHostSockPath(); got != "/home/someone/snap/chromium/3606/.reminal/browser.sock" {
		t.Errorf("in another snap: %s", got)
	}
	t.Setenv("SNAP_NAME", "")
	t.Setenv("HOME", "/home/someone")
	if got, _ := BrowserHostSockPath(); got != "/home/someone/.reminal/browser.sock" {
		t.Errorf("outside a snap: %s", got)
	}
}
