// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"reminal/internal/client"
)

// connectRig is a browserConnect with nothing real behind it: a hub that
// says what the test tells it to, a terminal that is a pipe, and a runner
// that only notes what it was asked to start.
type connectRig struct {
	t      *testing.T
	d      browserConnectDeps
	out    *bytes.Buffer
	typed  *io.PipeWriter
	hup    chan os.Signal
	mu     sync.Mutex
	state  client.BrowserStatus
	asked  int
	ran    [][]string
	runErr error
	// runFn, when set, is what each program started returns, by its argv.
	runFn func(argv []string) error
}

var (
	hubNever     = client.BrowserStatus{Daemon: true}
	hubConnected = client.BrowserStatus{Daemon: true, Connected: true, Family: "chrome", Last: time.Unix(1700000000, 0)}
	hubWas       = client.BrowserStatus{Daemon: true, Family: "chrome", Last: time.Unix(1700000000, 0)}
	hubDown      = client.BrowserStatus{}
	// hubOld: a daemon from before it could be asked, which did not answer.
	hubOld = client.BrowserStatus{Daemon: true, NoAnswer: true, Family: "chrome", Last: time.Unix(1700000000, 0)}
	// hubFilesGone: one connected before, and the extension's files are no
	// longer on this computer.
	hubFilesGone = client.BrowserStatus{Daemon: true, FilesGone: true, Family: "chromium", Last: time.Unix(1700000000, 0)}
	// hubFolderTaken: the same, and the folder they are written to is
	// someone else's.
	hubFolderTaken = client.BrowserStatus{Daemon: true, FolderTaken: true, Family: "chromium", Last: time.Unix(1700000000, 0)}
)

// oldDaemonLine is what integrate ends on with such a daemon.
const oldDaemonLine = "Not waiting for the extension: the reminal running in the background on this computer is an older one that cannot say whether the browser is connected. It is replaced by the new one within a minute of an upgrade; the extension connects by itself once it is loaded, and running `reminal integrate` again then confirms it.\n"

// wideTerminal: the lines these tests read are compared whole, so they are
// printed to a terminal wide enough to hold each on one line. That they wrap
// on a narrow one is TestBrowserConnectLinesFitANarrowTerminal.
func wideTerminal(t *testing.T) {
	t.Helper()
	was := integrateWidth
	integrateWidth = func() int { return 1000 }
	t.Cleanup(func() { integrateWidth = was })
}

func TestBrowserConnectLinesFitANarrowTerminal(t *testing.T) {
	r := newConnectRig(t, "darwin", hubConnected)
	integrateWidth = func() int { return 40 }
	bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	r.finish(bc)
	// And the state sentences of --check, the longest lines there are.
	var states bytes.Buffer
	for _, s := range []client.BrowserStatus{{}, {Daemon: true}, hubConnected, hubOld, hubFilesGone, hubFolderTaken, {Daemon: true, FilesGone: true, Family: "edge"}} {
		sayWrapped(&states, "", "    ", browserCheckLine(true, s))
		sayWrapped(&states, "", "    ", browserCheckLine(false, s))
	}
	for _, out := range []string{r.out.String(), states.String()} {
		if strings.TrimSpace(out) == "" {
			t.Fatal("nothing was said")
		}
		for _, ln := range strings.Split(out, "\n") {
			if len([]rune(ln)) > 40 {
				t.Errorf("wider than 40 columns: %q", ln)
			}
		}
	}
}

func newConnectRig(t *testing.T, goos string, state client.BrowserStatus) *connectRig {
	t.Helper()
	wideTerminal(t)
	r := &connectRig{t: t, out: &bytes.Buffer{}, hup: make(chan os.Signal, 1), state: state}
	in, typed := io.Pipe()
	r.typed = typed
	t.Cleanup(func() { typed.Close() })
	r.d = browserConnectDeps{
		goos: goos,
		tty:  func() bool { return true },
		status: func() client.BrowserStatus {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.asked++
			return r.state
		},
		run: func(c browserOpenCmd) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.ran = append(r.ran, c.argv)
			if r.runFn != nil {
				return r.runFn(c.argv)
			}
			return r.runErr
		},
		lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		exists:   func(string) bool { return true },
		getenv: func(k string) string {
			return map[string]string{"DISPLAY": ":0", "ProgramFiles": `C:\Program Files`, "ProgramFiles(x86)": `C:\Program Files (x86)`, "LOCALAPPDATA": `C:\Users\u\AppData\Local`}[k]
		},
		in:   in,
		out:  r.out,
		hup:  r.hup,
		wait: 30 * time.Second,
		poll: 5 * time.Millisecond,
	}
	return r
}

func (r *connectRig) set(s client.BrowserStatus) {
	r.mu.Lock()
	r.state = s
	r.mu.Unlock()
}

func (r *connectRig) commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, argv := range r.ran {
		out = append(out, strings.Join(argv, " "))
	}
	return out
}

// typeLine types a line at the terminal, as the person would.
func (r *connectRig) typeLine(s string) {
	go func() { _, _ = r.typed.Write([]byte(s + "\n")) }()
}

// finish runs c.finish and fails the test if it has not returned within a
// few seconds: no wait may hang.
func (r *connectRig) finish(c *browserConnect) time.Duration {
	r.t.Helper()
	start := time.Now()
	done := make(chan struct{})
	go func() { c.finish(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		r.t.Fatalf("finish did not return; so far:\n%s", r.out.String())
	}
	return time.Since(start)
}

// readerEnded: the goroutine reading the terminal is gone.
func (r *connectRig) readerEnded(c *browserConnect) bool {
	if c.readerDone == nil {
		return true
	}
	select {
	case <-c.readerDone:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

var chromeOnly = []chromeBrowser{{Name: "Google Chrome"}}

// A scripted run (-y, or not at a terminal), a removal, and a run with no
// browser here are asked nothing: no prompt, nothing opened, the hub not
// even asked.
func TestBrowserConnectIsOnlyForAPersonAtATerminal(t *testing.T) {
	for _, c := range []struct {
		name              string
		tty               bool
		remove, assumeYes bool
		browsers          []chromeBrowser
	}{
		{"-y", true, false, true, chromeOnly},
		{"not a terminal", false, false, false, chromeOnly},
		{"--remove", true, true, false, chromeOnly},
		{"no browser", true, false, false, nil},
	} {
		r := newConnectRig(t, "darwin", hubNever)
		r.d.tty = func() bool { return c.tty }
		bc := newBrowserConnectWith(r.d, "/home/u", c.browsers, c.remove, c.assumeYes)
		if bc.already() {
			t.Errorf("%s: counted as already connected", c.name)
		}
		r.finish(bc)
		if r.out.Len() != 0 || len(r.commands()) != 0 || r.asked != 0 || bc.readerDone != nil {
			t.Errorf("%s: said %q, ran %v, asked the hub %d times", c.name, r.out.String(), r.commands(), r.asked)
		}
	}
}

// With an extension already connected: one line saying so, no prompt,
// nothing opened.
func TestBrowserConnectAlreadyConnected(t *testing.T) {
	r := newConnectRig(t, "darwin", hubConnected)
	bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	if !bc.already() {
		t.Fatal("not counted as already connected")
	}
	r.finish(bc)
	if got := r.out.String(); got != "\nGoogle Chrome is already connected. Agents in your reminal sessions can use it now.\n" {
		t.Errorf("said %q", got)
	}
	if len(r.commands()) != 0 {
		t.Errorf("ran %v", r.commands())
	}
}

// Yes (Enter) opens the browser at its extensions page and, on a Mac, shows
// the folder in Finder; the wait ends when the extension connects, and says
// so.
func TestBrowserConnectOpensThenWaitsForTheConnection(t *testing.T) {
	r := newConnectRig(t, "darwin", hubNever)
	bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	r.typeLine("")
	go func() {
		// Connect once it is waiting.
		for i := 0; i < 400; i++ {
			r.mu.Lock()
			asked := r.asked
			r.mu.Unlock()
			if asked >= 3 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		r.set(hubConnected)
	}()
	if took := r.finish(bc); took > 4*time.Second {
		t.Errorf("took %v", took)
	}
	out := r.out.String()
	if !strings.Contains(out, "Open the extensions page in Google Chrome now? [Y/n] ") {
		t.Errorf("no prompt:\n%s", out)
	}
	if !strings.Contains(out, "Waiting for the extension to connect, for up to 2 minutes. Press Enter to stop waiting.\n") {
		t.Errorf("did not say it waits:\n%s", out)
	}
	if !strings.HasSuffix(out, "Google Chrome connected. Agents in your reminal sessions can use it now.\n") {
		t.Errorf("did not end on the connection:\n%s", out)
	}
	want := []string{"open -R " + chromeExtensionDir("/home/u"), "pgrep -x Google Chrome", "open -a Google Chrome chrome://extensions"}
	if got := r.commands(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("ran %q, want %q", got, want)
	}
}

// No opens nothing, and still waits; with no connection the wait ends at
// its time, on a line naming the likely reason and that nothing is lost.
func TestBrowserConnectWaitEndsAtItsTime(t *testing.T) {
	r := newConnectRig(t, "linux", hubNever)
	r.d.wait = 60 * time.Millisecond
	bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	r.typeLine("n")
	r.finish(bc)
	out := r.out.String()
	if len(r.commands()) != 0 {
		t.Errorf("ran %v after a no", r.commands())
	}
	if !strings.HasSuffix(out, "Not connected yet: most likely the extension is not loaded in Google Chrome yet. It connects by itself once it is loaded, so nothing is lost by not waiting.\n") {
		t.Errorf("at its time:\n%s", out)
	}
	if r.asked < 3 {
		t.Errorf("asked the hub only %d times while waiting", r.asked)
	}

	// One that connected before: the likely reason is a browser with no
	// window open.
	r = newConnectRig(t, "linux", hubWas)
	r.d.wait = 30 * time.Millisecond
	bc = newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	r.typeLine("n")
	r.finish(bc)
	if out := r.out.String(); !strings.Contains(out, "Not connected yet: most likely no browser window is open.") {
		t.Errorf("for a browser that connected before:\n%s", out)
	}
}

// With the extension's files gone, the wait ends on that and on what puts
// them back, not on a guess about a window.
func TestBrowserConnectWaitEndsOnTheFilesBeingGone(t *testing.T) {
	r := newConnectRig(t, "linux", hubFilesGone)
	r.d.wait = 30 * time.Millisecond
	bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	r.typeLine("n")
	r.finish(bc)
	out := r.out.String()
	if !strings.HasSuffix(out, "Not connected yet: the files of reminal's browser extension are no longer on this computer. Run `reminal integrate` again to put them back; nothing is lost by not waiting.\n") || strings.Contains(out, "most likely") {
		t.Errorf("with the files gone:\n%s", out)
	}

	// The folder is someone else's: moving it away comes first.
	r = newConnectRig(t, "linux", hubFolderTaken)
	r.d.wait = 30 * time.Millisecond
	bc = newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	r.typeLine("n")
	r.finish(bc)
	out = r.out.String()
	if !strings.HasSuffix(out, "Not connected yet: a folder named reminal-extension in your home folder is on this computer and is not reminal's. Move it away, then run `reminal integrate` again; nothing is lost by not waiting.\n") || strings.Contains(out, "most likely") || strings.Contains(out, "no longer on this computer") {
		t.Errorf("with the folder taken:\n%s", out)
	}
}

// The page a browser's program is opened at is the one the state sentences
// name for its family.
func TestBrowserProgramsOpenAtTheirFamilysPage(t *testing.T) {
	for name, want := range map[string]string{
		"Google Chrome": "chrome://extensions", "Google Chrome Beta": "chrome://extensions", "Chromium": "chrome://extensions",
		"Microsoft Edge": "edge://extensions", "Brave": "brave://extensions", "Arc": "chrome://extensions",
	} {
		p, ok := browserPrograms[name]
		if !ok || p.url() != want || client.BrowserExtensionsPage(p.family) != want {
			t.Errorf("%s: %q, want %q", name, p.url(), want)
		}
	}
	if got := client.BrowserExtensionsPage(""); got != "chrome://extensions" {
		t.Errorf("a family not known: %q", got)
	}
}

// The extension's files count as here only in a folder that holds reminal's
// extension: the one of now or the one of before. A folder that is missing,
// empty, or someone else's holds nothing.
func TestChromeExtensionHere(t *testing.T) {
	home := t.TempDir()
	now, before := chromeExtensionDir(home), chromeExtensionOldDir(home)
	is := func(name string, want client.ExtensionFiles) {
		t.Helper()
		if got := chromeExtensionFiles(home); got != want || chromeExtensionHere(home) != (want == client.ExtensionFilesHere) {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
	is("no folder", client.ExtensionFilesGone)
	if err := os.MkdirAll(now, 0o755); err != nil {
		t.Fatal(err)
	}
	is("an empty folder", client.ExtensionFilesGone)
	if err := os.WriteFile(filepath.Join(now, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	is("someone else's folder", client.ExtensionFilesTaken)
	if err := os.WriteFile(filepath.Join(now, "manifest.json"), []byte(`{"name":"other","key":"not reminal's"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	is("another extension in the folder", client.ExtensionFilesTaken)
	if err := writeChromeExtension(before); err != nil {
		t.Fatal(err)
	}
	is("the folder of before intact, someone else's of now", client.ExtensionFilesHere)
	if err := os.RemoveAll(before); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(now); err != nil {
		t.Fatal(err)
	}
	if err := writeChromeExtension(now); err != nil {
		t.Fatal(err)
	}
	is("the folder intact", client.ExtensionFilesHere)

	// The hook the hub and the commands look through reads the same, in
	// the home folder of the process.
	if runtime.GOOS != "windows" {
		t.Setenv("HOME", home)
		if got := client.BrowserExtensionFiles(); got != client.ExtensionFilesHere {
			t.Errorf("the hook, the folder intact: %d", got)
		}
		if err := os.RemoveAll(now); err != nil {
			t.Fatal(err)
		}
		if got := client.BrowserExtensionFiles(); got != client.ExtensionFilesGone {
			t.Errorf("the hook, no folder: %d", got)
		}
	}
}

// Enter ends the wait early, with the same line.
func TestBrowserConnectWaitEndsOnAKey(t *testing.T) {
	r := newConnectRig(t, "linux", hubNever)
	bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	go func() {
		_, _ = r.typed.Write([]byte("n\n"))
		_, _ = r.typed.Write([]byte("\n"))
	}()
	if took := r.finish(bc); took > 4*time.Second {
		t.Errorf("a key did not end the wait: %v", took)
	}
	if out := r.out.String(); !strings.Contains(out, "Waiting for the extension") || !strings.Contains(out, "Not connected yet: ") {
		t.Errorf("after a key:\n%s", out)
	}
}

// The terminal closing (stdin at its end) ends the wait at once and
// quietly, and the goroutine reading it is gone; so does it closing at the
// prompt, where nothing is opened.
func TestBrowserConnectEndsWhenTheTerminalCloses(t *testing.T) {
	r := newConnectRig(t, "linux", hubNever)
	bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	go func() {
		_, _ = r.typed.Write([]byte("n\n"))
		r.typed.Close()
	}()
	if took := r.finish(bc); took > 4*time.Second {
		t.Errorf("stdin closing did not end the wait: %v", took)
	}
	if out := r.out.String(); !strings.Contains(out, "Waiting for the extension") || strings.Contains(out, "Not connected yet") {
		t.Errorf("after stdin closed:\n%s", out)
	}
	if !r.readerEnded(bc) {
		t.Error("the reader of the terminal is still there")
	}

	r = newConnectRig(t, "darwin", hubNever)
	bc = newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	r.typed.Close()
	r.finish(bc)
	if out := r.out.String(); strings.Contains(out, "Waiting") || len(r.commands()) != 0 {
		t.Errorf("stdin closed at the prompt: said %q, ran %v", out, r.commands())
	}
	if !r.readerEnded(bc) {
		t.Error("the reader of the terminal is still there")
	}
}

// A hangup ends it too, at the prompt and in the wait; the reader ends once
// its terminal is gone.
func TestBrowserConnectEndsOnAHangup(t *testing.T) {
	r := newConnectRig(t, "linux", hubNever)
	bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	r.hup <- os.Interrupt
	r.finish(bc)
	if out := r.out.String(); strings.Contains(out, "Waiting") || len(r.commands()) != 0 {
		t.Errorf("a hangup at the prompt: said %q, ran %v", out, r.commands())
	}

	r = newConnectRig(t, "linux", hubNever)
	bc = newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	r.typeLine("n")
	go func() {
		time.Sleep(50 * time.Millisecond)
		r.hup <- os.Interrupt
	}()
	if took := r.finish(bc); took > 4*time.Second {
		t.Errorf("a hangup did not end the wait: %v", took)
	}
	if out := r.out.String(); strings.Contains(out, "Not connected yet") {
		t.Errorf("after a hangup:\n%s", out)
	}
	r.typed.Close()
	if !r.readerEnded(bc) {
		t.Error("the reader of the terminal is still there")
	}
}

// With no daemon there is nothing to wait for: it says so and ends.
func TestBrowserConnectDoesNotWaitWithNoDaemon(t *testing.T) {
	r := newConnectRig(t, "linux", hubDown)
	bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
	r.typeLine("n")
	r.finish(bc)
	out := r.out.String()
	if !strings.Contains(out, "reminal's daemon is not running on this computer. Starting a reminal session starts it (run `reminal new`); if it still is not running, run `reminal daemon --install`. The extension connects by itself once it is loaded and the daemon is running.\n") || strings.Contains(out, "Waiting") {
		t.Errorf("with no daemon:\n%s", out)
	}
}

// A browser that cannot be opened is said, with the page to go to, and the
// wait goes on.
func TestBrowserConnectSaysWhenTheBrowserDidNotOpen(t *testing.T) {
	r := newConnectRig(t, "windows", hubNever)
	r.runErr = errors.New("no such program")
	r.d.wait = 30 * time.Millisecond
	bc := newBrowserConnectWith(r.d, `C:\Users\u`, []chromeBrowser{{Name: "Microsoft Edge"}}, false, false)
	r.typeLine("y")
	r.finish(bc)
	out := r.out.String()
	if !strings.Contains(out, "Could not open Microsoft Edge (no such program): open it yourself and go to edge://extensions.\n") || !strings.Contains(out, "Waiting for the extension") || strings.Contains(out, "If the extensions page did not open") {
		t.Errorf("a browser that did not open:\n%s", out)
	}
}

// The program started is the browser's own, with its extensions page as an
// argument, on each system; where it is not found, or there is no desktop,
// nothing is offered.
func TestBrowserOpenCommands(t *testing.T) {
	ext := chromeExtensionDir("/home/u")
	for _, c := range []struct {
		goos, browser string
		want          []string
	}{
		{"darwin", "Google Chrome", []string{"open -R " + ext, "open -a Google Chrome chrome://extensions"}},
		{"darwin", "Google Chrome Beta", []string{"open -R " + ext, "open -a Google Chrome Beta chrome://extensions"}},
		{"darwin", "Microsoft Edge", []string{"open -R " + ext, "open -a Microsoft Edge edge://extensions"}},
		{"darwin", "Brave", []string{"open -R " + ext, "open -a Brave Browser brave://extensions"}},
		{"darwin", "Chromium", []string{"open -R " + ext, "open -a Chromium chrome://extensions"}},
		{"darwin", "Arc", []string{"open -R " + ext, "open -a Arc chrome://extensions"}},
		{"linux", "Google Chrome", []string{"/usr/bin/google-chrome chrome://extensions"}},
		{"linux", "Google Chrome Beta", []string{"/usr/bin/google-chrome-beta chrome://extensions"}},
		{"linux", "Chromium", []string{"/usr/bin/chromium chrome://extensions"}},
		{"linux", "Microsoft Edge", []string{"/usr/bin/microsoft-edge edge://extensions"}},
		{"linux", "Brave", []string{"/usr/bin/brave-browser brave://extensions"}},
		{"windows", "Google Chrome", []string{`C:\Program Files\Google\Chrome\Application\chrome.exe chrome://extensions`}},
		{"windows", "Google Chrome Beta", []string{`C:\Program Files\Google\Chrome Beta\Application\chrome.exe chrome://extensions`}},
		{"windows", "Chromium", []string{`C:\Program Files\Chromium\Application\chrome.exe chrome://extensions`}},
		{"windows", "Microsoft Edge", []string{`C:\Program Files\Microsoft\Edge\Application\msedge.exe edge://extensions`}},
		{"windows", "Brave", []string{`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe brave://extensions`}},
	} {
		r := newConnectRig(t, c.goos, hubNever)
		bc := newBrowserConnectWith(r.d, "/home/u", []chromeBrowser{{Name: c.browser}}, false, false)
		var got []string
		for _, cmd := range bc.openCmds(bc.browsers[0]) {
			got = append(got, strings.Join(cmd.argv, " "))
			if (c.goos == "darwin") != cmd.wait {
				t.Errorf("%s, %s: %v waited for: %v", c.goos, c.browser, cmd.argv, cmd.wait)
			}
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s, %s: %q, want %q", c.goos, c.browser, got, c.want)
		}
	}

	// On Windows the program is looked for where a browser installs itself:
	// here only under the person's own local app data.
	r := newConnectRig(t, "windows", hubNever)
	local := `C:\Users\u\AppData\Local\Google\Chrome\Application\chrome.exe`
	r.d.exists = func(p string) bool { return p == local }
	bc := newBrowserConnectWith(r.d, `C:\Users\u`, chromeOnly, false, false)
	if cmds := bc.openCmds(chromeOnly[0]); len(cmds) != 1 || cmds[0].argv[0] != local {
		t.Errorf("a browser in the local app data: %v", cmds)
	}

	// A second name for the program on Linux, when the first is not there.
	r = newConnectRig(t, "linux", hubNever)
	r.d.lookPath = func(name string) (string, error) {
		if name == "chromium-browser" {
			return "/snap/bin/chromium-browser", nil
		}
		return "", errors.New("not found")
	}
	bc = newBrowserConnectWith(r.d, "/home/u", []chromeBrowser{{Name: "Chromium"}}, false, false)
	if cmds := bc.openCmds(bc.browsers[0]); len(cmds) != 1 || cmds[0].argv[0] != "/snap/bin/chromium-browser" {
		t.Errorf("chromium by its other name: %v", cmds)
	}

	// Not found, or no desktop (a terminal over ssh): no prompt, and the
	// wait still runs.
	for _, why := range []string{"not found", "no desktop"} {
		r = newConnectRig(t, "linux", hubNever)
		if why == "not found" {
			r.d.lookPath = func(string) (string, error) { return "", errors.New("not found") }
		} else {
			r.d.getenv = func(string) string { return "" }
		}
		r.d.wait = 30 * time.Millisecond
		bc = newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
		r.finish(bc)
		if out := r.out.String(); strings.Contains(out, "Open the extensions page") || !strings.Contains(out, "Waiting for the extension") || len(r.commands()) != 0 {
			t.Errorf("%s: said %q, ran %v", why, out, r.commands())
		}
	}
}

// Over SSH the person is not at this computer's screen: no offer, nothing
// opened or shown in Finder, on any system; the wait and its confirmation
// stay.
func TestBrowserConnectOffersNothingOverSSH(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, key := range []string{"SSH_CONNECTION", "SSH_TTY"} {
			r := newConnectRig(t, goos, hubNever)
			was := r.d.getenv
			r.d.getenv = func(k string) string {
				if k == key {
					return "set"
				}
				return was(k)
			}
			bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, false)
			go func() {
				time.Sleep(40 * time.Millisecond)
				r.set(hubConnected)
			}()
			r.finish(bc)
			out := r.out.String()
			if strings.Contains(out, "Open the extensions page") || len(r.commands()) != 0 {
				t.Errorf("%s, %s: said %q, ran %v", goos, key, out, r.commands())
			}
			if !strings.Contains(out, "Waiting for the extension") || !strings.HasSuffix(out, "Google Chrome connected. Agents in your reminal sessions can use it now.\n") {
				t.Errorf("%s, %s: the wait:\n%s", goos, key, out)
			}
		}
	}
}

// The browser that connected last is the one opened, when it is among those
// set up; else the first.
func TestBrowserConnectPicksTheBrowserThatConnectedLast(t *testing.T) {
	both := []chromeBrowser{{Name: "Google Chrome"}, {Name: "Microsoft Edge"}}
	was := hubWas
	was.Family = "edge"
	r := newConnectRig(t, "darwin", was)
	bc := newBrowserConnectWith(r.d, "/home/u", both, false, false)
	if b, _ := bc.pick(); b.Name != "Microsoft Edge" {
		t.Errorf("picked %s", b.Name)
	}
	r = newConnectRig(t, "darwin", hubNever)
	bc = newBrowserConnectWith(r.d, "/home/u", both, false, false)
	if b, _ := bc.pick(); b.Name != "Google Chrome" {
		t.Errorf("picked %s", b.Name)
	}
}

// --check's line says which state it is, each in its own sentence.
func TestBrowserCheckLineSaysTheState(t *testing.T) {
	for _, c := range []struct {
		name  string
		setUp bool
		s     client.BrowserStatus
		want  string
	}{
		{"not set up", false, hubNever, "  · The browser is not set up for reminal on this computer: run `reminal integrate`"},
		{"no daemon", true, hubDown, "  ! reminal's daemon is not running on this computer, so the browser cannot be reached: starting a reminal session starts it (run `reminal new`); if it still is not running, run `reminal daemon --install`. Then try again."},
		{"files gone", true, hubFilesGone, "  ! The files of reminal's browser extension are no longer on this computer: run `reminal integrate` to put them back, then open a browser window; if the extension still does not connect, reload it on Chromium's extensions page (chrome://extensions)."},
		{"files gone, edge", true, client.BrowserStatus{Daemon: true, FilesGone: true, Family: "edge"}, "  ! The files of reminal's browser extension are no longer on this computer: run `reminal integrate` to put them back, then open a browser window; if the extension still does not connect, reload it on Microsoft Edge's extensions page (edge://extensions)."},
		{"files gone, brave", true, client.BrowserStatus{Daemon: true, FilesGone: true, Family: "brave"}, "  ! The files of reminal's browser extension are no longer on this computer: run `reminal integrate` to put them back, then open a browser window; if the extension still does not connect, reload it on Brave's extensions page (brave://extensions)."},
		{"files gone, no family", true, client.BrowserStatus{Daemon: true, FilesGone: true}, "  ! The files of reminal's browser extension are no longer on this computer: run `reminal integrate` to put them back, then open a browser window; if the extension still does not connect, reload it on the browser's extensions page (chrome://extensions)."},
		{"folder taken", true, hubFolderTaken, "  ! A folder named reminal-extension in your home folder is on this computer and is not reminal's, so the files of reminal's browser extension cannot be put there: move it away, then run `reminal integrate`."},
		{"files gone, nothing set up", false, hubFilesGone, "  · The browser is not set up for reminal on this computer"},
		{"no answer", true, hubOld, "  ! The reminal running in the background on this computer is an older one that cannot say whether the browser is connected: it is replaced by the new one within a minute of an upgrade, then run this again."},
		{"never connected", true, hubNever, "  ! reminal's browser extension is not connected, and has not connected before: load it in your browser"},
		{"was connected", true, hubWas, "  ! reminal's browser extension is not connected now, though Google Chrome was connected before: the browser is closed or has no window open, so open a browser window"},
		{"connected", true, hubConnected, "  ✓ Google Chrome is connected: agents in your reminal sessions on this computer can use it."},
	} {
		if got := browserCheckLine(c.setUp, c.s); !strings.HasPrefix(got, c.want) || strings.Contains(got, "\n") {
			t.Errorf("%s: %q", c.name, got)
		}
	}
}

// atTerminal stands a person at a terminal in for the length of a test of
// runIntegrate: the hub says state, what they type is typed, and whatever
// would be started is only noted.
func atTerminal(t *testing.T, state client.BrowserStatus, typed string) *[]string {
	t.Helper()
	wideTerminal(t)
	in, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString(typed)
	w.Close()
	wasIn, wasDeps, wasStatus := os.Stdin, browserConnectDepsNow, browserStatusNow
	os.Stdin = in
	var ran []string
	browserStatusNow = func() client.BrowserStatus { return state }
	browserConnectDepsNow = func() browserConnectDeps {
		d := wasDeps()
		d.goos = "darwin"
		d.tty = func() bool { return true }
		d.status = browserStatusNow
		d.run = func(c browserOpenCmd) error { ran = append(ran, strings.Join(c.argv, " ")); return nil }
		d.wait, d.poll = 50*time.Millisecond, 5*time.Millisecond
		return d
	}
	t.Cleanup(func() {
		os.Stdin, browserConnectDepsNow, browserStatusNow = wasIn, wasDeps, wasStatus
		in.Close()
	})
	return &ran
}

// Through `reminal integrate` itself, by a person at a terminal: with an
// extension already connected the steps are not given, only the line saying
// so; with none, the steps come first and then the question. With -y neither
// is said and nothing is opened. --check says the state.
func TestIntegrateAtATerminalEndsOnTheBrowsersConnection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows this writes the person's own registry")
	}
	home := t.TempDir()
	chrome := chromeBrowserTable()[0]
	if err := os.MkdirAll(filepath.Join(home, chrome.Dir), 0o755); err != nil {
		t.Fatal(err)
	}

	check := runIntegrateIn(t, home, "--check")
	if !strings.Contains(check, "The browser is not set up for reminal on this computer") {
		t.Errorf("--check before a setup:\n%s", check)
	}

	// The terminal closes at the question: it is asked after the steps,
	// and nothing is opened.
	ran := atTerminal(t, hubNever, "y\n")
	out := runIntegrateIn(t, home)
	steps := strings.Index(out, "Load unpacked")
	asked := strings.Index(out, "Open the extensions page in Google Chrome now? [Y/n] ")
	if steps < 0 || asked < steps || len(*ran) != 0 {
		t.Errorf("with no extension connected (ran %v):\n%s", *ran, out)
	}

	ran = atTerminal(t, hubConnected, "y\n")
	out = runIntegrateIn(t, home)
	if strings.Contains(out, "Load unpacked") || strings.Contains(out, "Open the extensions page") || len(*ran) != 0 ||
		!strings.HasSuffix(out, "\nGoogle Chrome is already connected. Agents in your reminal sessions can use it now.\n") {
		t.Errorf("with an extension connected (ran %v):\n%s", *ran, out)
	}

	ran = atTerminal(t, hubNever, "")
	out = runIntegrateIn(t, home, "-y")
	if strings.Contains(out, "Open the extensions page") || strings.Contains(out, "Waiting") || len(*ran) != 0 || !strings.Contains(out, "Load unpacked") {
		t.Errorf("with -y (ran %v):\n%s", *ran, out)
	}

	for _, c := range []struct {
		state client.BrowserStatus
		want  string
	}{
		{hubDown, "reminal's daemon is not running on this computer"},
		{hubNever, "is not connected, and has not connected before"},
		{hubWas, "is not connected now, though Google Chrome was connected before"},
		{hubConnected, "Google Chrome is connected: agents in your reminal sessions"},
	} {
		atTerminal(t, c.state, "")
		if check := runIntegrateIn(t, home, "--check"); !strings.Contains(check, c.want) {
			t.Errorf("--check does not say %q:\n%s", c.want, check)
		}
	}
}
