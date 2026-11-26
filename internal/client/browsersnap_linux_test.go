// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func unixMkfifo(path string) error { return unix.Mkfifo(path, 0o600) }

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
		// A copy put behind the link by hand, and a setup's note: the hub
		// still opens nothing.
		if err := os.WriteFile(filepath.Join(elsewhere, "reminal"), []byte("x"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeNote(t, home)
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

// writeNote writes the note a setup leaves in ~/.reminal, by hand.
func writeNote(t *testing.T, home string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".reminal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapBrowserNote(home), []byte("chromium\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapHub is a hub serving in home, as the daemon's does, and what asks it.
func snapHub(t *testing.T, home string, setup ...func(h *browserHub)) (h *browserHub, main string, ask func(path, line string) string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("REMINAL_BROWSER_SOCK", "")
	h = newBrowserHub()
	h.noWait = true
	// Set before it serves: nothing of the hub's is written once it runs.
	for _, f := range setup {
		f(h)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go serveBrowserOn(h, stop)
	main = filepath.Join(home, ".reminal", "browser.sock")
	for i := 0; i < 300; i++ {
		if c, err := net.Dial("unix", main); err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ask = func(path, line string) string {
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
	return h, main, ask
}

// until waits for ok, for up to two seconds.
func until(ok func() bool) bool {
	for i := 0; i < 200; i++ {
		if ok() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ok()
}

func isSock(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode()&os.ModeSocket != 0
}

// The hub's second socket: not there until a setup is, open to the person
// alone, served as the first is (a state is answered; a program that is not
// reminal's helper under a browser is not taken in as one), and gone when
// the setup is taken away.
func TestBrowserHubServesASnapBrowsersSocket(t *testing.T) {
	home, exe := snapHome(t)
	h, main, ask := snapHub(t, home)
	sock := filepath.Join(SnapBrowserDir(home), "browser.sock")
	ask(main, `{"t":"state"}`)
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Lstat(SnapBrowserDir(home)); !os.IsNotExist(err) {
		t.Fatalf("with no setup the hub made the snap's folder: %v", err)
	}
	if err := SnapBrowserSetUp(home, exe); err != nil {
		t.Fatal(err)
	}
	// A setup asks the hub its state when it is done: the socket opens then.
	ask(main, `{"t":"state"}`)
	if !until(func() bool { return isSock(sock) }) {
		t.Fatal("no socket in the snap's folder after a setup")
	}
	if st, _ := os.Lstat(sock); st.Mode().Perm() != 0o600 {
		t.Errorf("the snap's socket is %v, want 0600", st.Mode().Perm())
	}
	if got := ask(sock, `{"t":"state"}`); !strings.Contains(got, "connected") {
		t.Errorf("a state asked on the snap's socket: %q", got)
	}
	// This test is no helper under a browser: as on the first socket, it is
	// not taken in as the bridge.
	ask(sock, `{"t":"host"}`)
	h.mu.Lock()
	taken := h.host != nil || len(h.standby) > 0
	h.mu.Unlock()
	if taken {
		t.Error("a program that is not reminal's helper was taken in as a bridge on the snap's socket")
	}
	// A session's request there is checked as on the first: this process is
	// no session.
	if got := ask(sock, `{"t":"req","session":"ABCD1234","tool":"browser_tabs_context"}`); !strings.Contains(got, "itself may ask") {
		t.Errorf("a request from a process that is not the session, on the snap's socket: %q", got)
	}
	if err := SnapBrowserRemove(home); err != nil {
		t.Fatal(err)
	}
	ask(main, `{"t":"state"}`)
	if !until(func() bool { _, err := net.Dial("unix", sock); return err != nil }) {
		t.Error("the snap's socket still answers after the setup was taken away")
	}
}

// What is in the snap's folder never decides whether the hub serves there:
// a folder and a copy put there by hand (the snap browser can), with no
// setup made by integrate, open no socket, and the copy is left as it is.
func TestTheSnapsFolderAloneOpensNoSocket(t *testing.T) {
	home, _ := snapHome(t)
	dir := SnapBrowserDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reminal"), []byte("not reminal"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, main, ask := snapHub(t, home)
	ask(main, `{"t":"state"}`)
	time.Sleep(300 * time.Millisecond)
	if isSock(filepath.Join(dir, "browser.sock")) {
		t.Error("a socket was opened in the snap's folder with no setup made")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "reminal")); string(got) != "not reminal" {
		t.Errorf("the file there was written over (%d bytes now)", len(got))
	}
	if there, _ := SnapBrowserSetUpDone(home, "/proc/self/exe"); there {
		t.Error("a setup is said to be there")
	}
}

// Something that is not a file in the copy's place (a pipe nobody writes
// to, which an open would wait on for ever) holds nothing up: the hub's
// first socket answers, the check of a setup returns, and a setup writes
// the copy in its place.
func TestAPipeInTheCopysPlaceHoldsNothingUp(t *testing.T) {
	home, exe := snapHome(t)
	dir := SnapBrowserDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unixMkfifo(filepath.Join(dir, "reminal")); err != nil {
		t.Fatal(err)
	}
	writeNote(t, home)
	done := make(chan bool, 1)
	go func() {
		_, current := SnapBrowserSetUpDone(home, exe)
		done <- current
	}()
	select {
	case current := <-done:
		if current {
			t.Error("a pipe was taken for the copy")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the check of a setup waited on the pipe")
	}
	_, main, ask := snapHub(t, home)
	for i := 0; i < 3; i++ {
		if got := ask(main, `{"t":"state"}`); !strings.Contains(got, "connected") {
			t.Fatalf("the hub's first socket did not answer with a pipe in the copy's place: %q", got)
		}
	}
	set := make(chan error, 1)
	go func() { set <- SnapBrowserSetUp(home, exe) }()
	select {
	case err := <-set:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a setup waited on the pipe")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "reminal")); string(got) != "reminal one" {
		t.Errorf("the copy after a setup over a pipe: %q", got)
	}
}

// A file of another size in the copy's place is not read: one of a million
// million bytes (holes, no disk used) is told apart at once.
func TestAHugeFileInTheCopysPlaceIsNotRead(t *testing.T) {
	home, exe := snapHome(t)
	dir := SnapBrowserDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "reminal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 40); err != nil {
		f.Close()
		t.Skipf("no file with holes here: %v", err)
	}
	f.Close()
	writeNote(t, home)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	_, current := SnapBrowserSetUpDone(home, exe)
	runtime.ReadMemStats(&after)
	if current {
		t.Error("the huge file was taken for the copy")
	}
	if grew := int64(after.TotalAlloc) - int64(before.TotalAlloc); grew > 8<<20 || time.Since(start) > 2*time.Second {
		t.Errorf("the huge file was read: %d bytes taken, %v", grew, time.Since(start))
	}
	d, err := openOwnDir(home, snapBrowserParts(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := readIn(d, "reminal", snapManifestMax); err == nil {
		t.Error("a file over the bound was read")
	}
}

// The socket's name with a link in its place: what the link leads to keeps
// its mode.
func TestALinkInTheSocketsPlaceIsNotFollowed(t *testing.T) {
	home, exe := snapHome(t)
	if err := SnapBrowserSetUp(home, exe); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(SnapBrowserDir(home), "browser.sock")); err != nil {
		t.Fatal(err)
	}
	d, err := openOwnDir(home, snapBrowserParts(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	closeSockToOthers(d, "browser.sock")
	if st, _ := os.Stat(target); st.Mode().Perm() != 0o644 {
		t.Errorf("the file the link leads to was changed to %v", st.Mode().Perm())
	}
}

// A remove takes the setup away whatever the snap browser did to the folder
// since: its mode changed, files added. The note goes first, so nothing is
// served there after.
func TestRemoveWhateverWasDoneToTheFolder(t *testing.T) {
	home, exe := snapHome(t)
	if err := SnapBrowserSetUp(home, exe); err != nil {
		t.Fatal(err)
	}
	dir := SnapBrowserDir(home)
	if err := os.WriteFile(filepath.Join(dir, "extra"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := SnapBrowserRemove(home); err != nil {
		t.Fatal(err)
	}
	if SnapBrowserEnabled(home) {
		t.Error("the note is still there after a remove")
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder is still there after a remove: %v", err)
	}
	if _, _, err := snapBrowserListen(home); err == nil {
		t.Error("the hub would still serve there after a remove")
	}
}

// eachABrowser has the hub take every connection in as the helper of
// another browser.
func eachABrowser(h *browserHub) {
	var n atomic.Int64
	h.allowBridge = func(int) bool { return true }
	h.testBrowser = func(int) int { return int(n.Add(1)) }
}

// bridge connects to the hub as a browser's helper would and waits until
// the hub has taken it in.
func bridge(t *testing.T, h *browserHub, sock string) net.Conn {
	t.Helper()
	count := func() int {
		h.mu.Lock()
		defer h.mu.Unlock()
		n := len(h.standby)
		if h.host != nil {
			n++
		}
		return n
	}
	was := count()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Write([]byte(`{"t":"host"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	until(func() bool { return count() != was })
	return c
}

// Two browsers running with the extension at once do not take the bridge
// from each other: the first stays in use, the second stands by and becomes
// the bridge when the first goes. A bridge on the snap browser's socket
// never takes over from one on the hub's own; one on the hub's own does
// take over from the snap's.
func TestBridgesOfTwoBrowsersDoNotStealFromEachOther(t *testing.T) {
	home, exe := snapHome(t)
	h, main, ask := snapHub(t, home, eachABrowser)
	if err := SnapBrowserSetUp(home, exe); err != nil {
		t.Fatal(err)
	}
	ask(main, `{"t":"state"}`)
	sock := filepath.Join(SnapBrowserDir(home), "browser.sock")
	if !until(func() bool { return isSock(sock) }) {
		t.Fatal("no socket in the snap's folder")
	}
	inUse := func() (snap bool, standby int) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.host == nil {
			t.Fatal("no bridge in use")
		}
		return h.host.snap, len(h.standby)
	}
	first := bridge(t, h, main)
	bridge(t, h, main) // another browser, on the same socket
	h.mu.Lock()
	kept := h.host
	h.mu.Unlock()
	if _, standby := inUse(); standby != 1 {
		t.Fatalf("a second browser's bridge: %d on standby, want 1 (the first kept in use)", standby)
	}
	bridge(t, h, sock) // the snap browser
	h.mu.Lock()
	still := h.host == kept
	h.mu.Unlock()
	if snap, standby := inUse(); snap || !still || standby != 2 {
		t.Fatalf("a bridge on the snap's socket took over: in use is the snap's %v, the first kept %v, %d on standby", snap, still, standby)
	}
	// The one in use goes: the next is one on the hub's own socket.
	first.Close()
	if !until(func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.host != nil && h.host != kept }) {
		t.Fatal("no bridge took over when the one in use went")
	}
	if snap, standby := inUse(); snap || standby != 1 {
		t.Errorf("after the first went: in use is the snap's %v, %d on standby (want the other browser's, 1)", snap, standby)
	}
}

// The snap's bridge in use, then a browser on the hub's own socket: that
// one is used, and the snap's stands by.
func TestABridgeOnTheHubsOwnSocketTakesOverFromTheSnaps(t *testing.T) {
	home, exe := snapHome(t)
	h, main, ask := snapHub(t, home, eachABrowser)
	if err := SnapBrowserSetUp(home, exe); err != nil {
		t.Fatal(err)
	}
	ask(main, `{"t":"state"}`)
	sock := filepath.Join(SnapBrowserDir(home), "browser.sock")
	if !until(func() bool { return isSock(sock) }) {
		t.Fatal("no socket in the snap's folder")
	}
	bridge(t, h, sock)
	h.mu.Lock()
	snapFirst := h.host != nil && h.host.snap
	h.mu.Unlock()
	bridge(t, h, main)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !snapFirst || h.host == nil || h.host.snap || len(h.standby) != 1 || !h.standby[0].snap {
		t.Errorf("want the hub's own bridge in use and the snap's on standby: snap first %v, in use %+v, standby %d", snapFirst, h.host, len(h.standby))
	}
}

// With the snap browser's bridge in use, a file it may not read is said so
// before anything is asked of the browser.
func TestTheSnapBrowserIsNotAskedForAFileItCannotRead(t *testing.T) {
	home := "/home/someone"
	for path, can := range map[string]bool{
		"/home/someone/Documents/a.txt": true,
		"/home/someone/a.txt":           true,
		"/media/someone/stick/a.txt":    true,
		"/home/someone/.config/a.txt":   false,
		"/tmp/a.txt":                    false,
		"/var/tmp/a.txt":                false,
		"/home/other/a.txt":             false,
		"/home/someone":                 false,
	} {
		if got := snapBrowserCanRead(home, path); got != can {
			t.Errorf("%s: can read %v, want %v", path, got, can)
		}
	}
	got := browserArgPaths("file_upload", []byte(`{"ref":"e1","paths":["/tmp/a.txt","/home/someone/b.txt"]}`))
	if len(got) != 2 || got[0] != "/tmp/a.txt" {
		t.Errorf("the paths of an upload: %v", got)
	}
	got = browserArgPaths("batch", []byte(`{"steps":[{"tool":"click","args":{"ref":"e2"}},{"tool":"file_upload","args":{"paths":["/tmp/c.txt"]}}]}`))
	if len(got) != 1 || got[0] != "/tmp/c.txt" {
		t.Errorf("the paths of an upload in a batch: %v", got)
	}
	if got := browserArgPaths("click", []byte(`{"paths":["/tmp/a.txt"]}`)); got != nil {
		t.Errorf("another tool's arguments were read as files: %v", got)
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

// A look at the snap's folder asked for while another is under way is not
// lost: a remove that lands then still has the socket closed, with no
// further request.
func TestALookAskedForWhileOneIsUnderWayIsNotLost(t *testing.T) {
	home, exe := snapHome(t)
	h, main, ask := snapHub(t, home)
	if err := SnapBrowserSetUp(home, exe); err != nil {
		t.Fatal(err)
	}
	ask(main, `{"t":"state"}`)
	sock := filepath.Join(SnapBrowserDir(home), "browser.sock")
	if !until(func() bool { return isSock(sock) }) {
		t.Fatal("no socket in the snap's folder")
	}
	// The next look is held when it has looked (and found nothing to do),
	// with the lock still its.
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.snapMu.Lock()
	h.snapHold = func() { once.Do(func() { close(held); <-release }) }
	h.snapMu.Unlock()
	ask(main, `{"t":"state"}`)
	<-held
	// The setup is taken away, and its state request lands meanwhile.
	if err := os.Remove(snapBrowserNote(home)); err != nil {
		t.Fatal(err)
	}
	ask(main, `{"t":"state"}`)
	time.Sleep(100 * time.Millisecond)
	close(release)
	if !until(func() bool { _, err := net.Dial("unix", sock); return err != nil }) {
		t.Error("the snap's socket stayed open: the look asked for meanwhile was lost")
	}
}

// The bridge on the hub's own socket goes and comes back (a browser's
// extension reconnects often): the snap browser's, on standby, is not used
// in that moment, and the one that came back is. Only when none comes back
// is the snap's used.
func TestTheSnapsBridgeWaitsForTheHubsOwnToComeBack(t *testing.T) {
	const grace = 600 * time.Millisecond
	home, exe := snapHome(t)
	h, main, ask := snapHub(t, home, eachABrowser, func(h *browserHub) { h.snapGrace = grace })
	if err := SnapBrowserSetUp(home, exe); err != nil {
		t.Fatal(err)
	}
	ask(main, `{"t":"state"}`)
	sock := filepath.Join(SnapBrowserDir(home), "browser.sock")
	if !until(func() bool { return isSock(sock) }) {
		t.Fatal("no socket in the snap's folder")
	}
	inUse := func() *browserConn { h.mu.Lock(); defer h.mu.Unlock(); return h.host }
	first := bridge(t, h, main)
	bridge(t, h, sock)
	kept := inUse()
	first.Close()
	if !until(func() bool { return inUse() != kept }) {
		t.Fatal("the bridge that went is still in use")
	}
	if b := inUse(); b != nil {
		t.Fatalf("the snap's bridge was used the moment the hub's own went (snap %v)", b.snap)
	}
	// It comes back within the moment: it is the one used, then and after.
	back := bridge(t, h, main)
	if b := inUse(); b == nil || b.snap {
		t.Fatalf("the bridge that came back is not the one in use: %+v", b)
	}
	time.Sleep(grace + 200*time.Millisecond)
	if b := inUse(); b == nil || b.snap {
		t.Errorf("after the moment the snap's bridge took over from the one that came back: %+v", b)
	}
	// It goes for good: the snap's is used once the moment is over.
	back.Close()
	if !until(func() bool { b := inUse(); return b != nil && b.snap }) {
		t.Error("with no bridge coming back, the snap's was not used")
	}
}

// At most browserStandbyMax bridges stand by (the oldest is let go), and
// each is told of a session that ended, as the one in use is.
func TestStandbysAreFewAndToldOfEndedSessions(t *testing.T) {
	home, _ := snapHome(t)
	h, main, _ := snapHub(t, home, eachABrowser)
	bridge(t, h, main) // in use
	var standby []net.Conn
	for i := 0; i < browserStandbyMax+2; i++ {
		standby = append(standby, bridge(t, h, main))
	}
	h.mu.Lock()
	got := len(h.standby)
	h.mu.Unlock()
	if got != browserStandbyMax {
		t.Fatalf("%d bridges stand by, want at most %d", got, browserStandbyMax)
	}
	// The oldest two were let go: their connections are closed.
	for i := 0; i < 2; i++ {
		_ = standby[i].SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := standby[i].Read(make([]byte, 1)); err == nil || os.IsTimeout(err) {
			t.Errorf("standby %d, over the most kept, was not let go (%v)", i, err)
		}
	}
	// A session that ended: said to one on standby too.
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.seen["ABCD1234"] = gone.Process.Pid
	h.mu.Unlock()
	h.tellEnded()
	last := standby[len(standby)-1]
	_ = last.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, _ := bufio.NewReader(last).ReadString('\n')
	if !strings.Contains(line, `"ended"`) || !strings.Contains(line, "ABCD1234") {
		t.Errorf("a bridge on standby was not told of the session that ended: %q", line)
	}
}
