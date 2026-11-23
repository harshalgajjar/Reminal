// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// The end of `reminal integrate` for the browser, when a person runs it at a
// terminal: setting up ends with the extension connected, or with a line
// saying why it is not yet. After the steps are printed it offers to open
// the browser (on a Mac at its extensions page, with the extension's folder
// shown in Finder; elsewhere the browser alone, with a line saying where to
// go in it), then waits for the extension to connect and says so.
// With an extension already connected it says that and gives no steps.
// With a daemon from before it could be asked (still running just after an
// upgrade) it gives the steps, does not wait, and says that the new one
// takes its place shortly.
//
// A scripted run (-y, or stdin or stdout not a terminal) is asked nothing,
// opens nothing and waits for nothing.
//
// The state comes from the daemon's browser hub (internal/client,
// browserstate.go); `reminal integrate --check` says it too.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"reminal/internal/client"
)

func init() {
	// The hub's refusal tells "not set up" apart by this: only the command
	// knows where each browser keeps its host manifests.
	client.BrowserSetUp = browserToolsHere
	// And "the extension's files are gone" by this: only the command knows
	// the folders they are written to. Set here, in the program's own
	// start, so the daemon's hub has it as every other reminal process does.
	client.BrowserExtensionFiles = browserExtensionFilesNow
}

// browserExtensionFilesNow is what is found where the extension's files
// live. With no home folder to look in they count as here, so nothing is
// said that was not seen.
func browserExtensionFilesNow() client.ExtensionFiles {
	home, err := os.UserHomeDir()
	if err != nil {
		return client.ExtensionFilesHere
	}
	return chromeExtensionFiles(home)
}

// chromeExtensionFiles: here when reminal's extension is in a folder a
// browser loads it from, the one of now or the one of before. Else taken
// when the folder of now is one a setup would not write over (it is there,
// not empty, and not reminal's: chromeExtensionOurs), and gone when a setup
// would write it again.
func chromeExtensionFiles(home string) client.ExtensionFiles {
	switch {
	case chromeExtensionHere(home):
		return client.ExtensionFilesHere
	case !chromeExtensionOurs(chromeExtensionDir(home)):
		return client.ExtensionFilesTaken
	}
	return client.ExtensionFilesGone
}

// browserConnectWait is how long integrate waits for the extension to
// connect, and browserConnectPoll how often it asks the hub meanwhile.
const (
	browserConnectWait = 2 * time.Minute
	browserConnectPoll = time.Second
)

// browserStatusNow asks the hub for the browser's state. A variable for
// tests.
var browserStatusNow = client.QueryBrowserStatus

// browserCheckLine is --check's line for the browser's connection.
func browserCheckLine(setUp bool, s client.BrowserStatus) string {
	mark := "!"
	switch s.State(setUp) {
	case client.BrowserConnected:
		mark = "✓"
	case client.BrowserNotSetUp:
		mark = "·"
	}
	return "  " + mark + " " + s.Sentence(setUp)
}

// printBrowserConnectCheck prints it, where there is a browser to speak of.
func printBrowserConnectCheck(home string) {
	if len(chromeBrowsers(home)) == 0 {
		return
	}
	sayWrapped(os.Stdout, "", "    ", browserCheckLine(len(chromeOurs(home)) > 0, browserStatusNow()))
}

// browserProgram is how one browser of chromeBrowserTable is opened: on a
// Mac at its extensions page, elsewhere by starting its own program, where
// the person goes to the page (openCmds). The system's URL opener does not
// take chrome:// links.
type browserProgram struct {
	family string   // as the hub's marker names it
	macApp string   // the application's name, for `open -a`
	linux  []string // its program's names on PATH
	win    string   // its program under Program Files or the local app data
}

// url is its extensions page: the one the state sentences name for its
// family, so the two cannot differ.
func (p browserProgram) url() string { return client.BrowserExtensionsPage(p.family) }

var browserPrograms = map[string]browserProgram{
	"Google Chrome":      {"chrome", "Google Chrome", []string{"google-chrome", "google-chrome-stable"}, `Google\Chrome\Application\chrome.exe`},
	"Google Chrome Beta": {"chrome", "Google Chrome Beta", []string{"google-chrome-beta"}, `Google\Chrome Beta\Application\chrome.exe`},
	"Chromium":           {"chromium", "Chromium", []string{"chromium", "chromium-browser"}, `Chromium\Application\chrome.exe`},
	"Microsoft Edge":     {"edge", "Microsoft Edge", []string{"microsoft-edge", "microsoft-edge-stable"}, `Microsoft\Edge\Application\msedge.exe`},
	"Brave":              {"brave", "Brave Browser", []string{"brave-browser", "brave"}, `BraveSoftware\Brave-Browser\Application\brave.exe`},
	"Arc":                {"arc", "Arc", nil, ""},
}

// browserOpenCmd is one program to start. wait: it returns at once and its
// exit says whether it worked (macOS's open); otherwise it is the browser
// itself, started apart from this terminal and left running. optional: its
// failure is not worth a line.
type browserOpenCmd struct {
	argv     []string
	wait     bool
	optional bool
}

// browserConnectDeps is everything the offer and the wait touch outside
// themselves, so a test never starts a program or needs a daemon.
type browserConnectDeps struct {
	goos     string
	tty      func() bool // stdin and stdout are both a terminal
	status   func() client.BrowserStatus
	run      func(browserOpenCmd) error
	lookPath func(string) (string, error)
	exists   func(string) bool
	getenv   func(string) string
	in       io.Reader
	out      io.Writer
	hup      <-chan os.Signal // the terminal went away; nil: SIGHUP and SIGTERM
	wait     time.Duration
	poll     time.Duration
}

// browserConnectDepsNow is the real ones. A variable for tests.
var browserConnectDepsNow = func() browserConnectDeps {
	return browserConnectDeps{
		goos: chromeOS,
		tty: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
		},
		status:   browserStatusNow,
		run:      browserOpenRun,
		lookPath: exec.LookPath,
		exists: func(p string) bool {
			st, err := os.Stat(p)
			return err == nil && !st.IsDir()
		},
		getenv: os.Getenv,
		in:     os.Stdin,
		out:    os.Stdout,
		wait:   browserConnectWait,
		poll:   browserConnectPoll,
	}
}

// browserOpenRun starts one program for real.
func browserOpenRun(c browserOpenCmd) error {
	if c.wait {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, c.argv[0], c.argv[1:]...).Run()
	}
	cmd := exec.Command(c.argv[0], c.argv[1:]...)
	browserDetach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// browserConnect is one run's offer and wait.
type browserConnect struct {
	d        browserConnectDeps
	home     string
	browsers []chromeBrowser
	// active: a person is at the terminal and a browser was just set up.
	active bool
	// first is the state as integrate found it (asked only when active).
	first client.BrowserStatus
	// readerDone closes when the goroutine reading the terminal has ended.
	readerDone chan struct{}
}

func newBrowserConnect(home string, browsers []chromeBrowser, remove, assumeYes bool) *browserConnect {
	return newBrowserConnectWith(browserConnectDepsNow(), home, browsers, remove, assumeYes)
}

func newBrowserConnectWith(d browserConnectDeps, home string, browsers []chromeBrowser, remove, assumeYes bool) *browserConnect {
	c := &browserConnect{d: d, home: home, browsers: browsers}
	c.active = !remove && !assumeYes && len(browsers) > 0 && d.tty()
	if c.active {
		c.first = d.status()
	}
	return c
}

// already: an extension is connected as integrate runs, so there are no
// steps to give and nothing to ask.
func (c *browserConnect) already() bool { return c.active && c.first.Connected }

// openCmds are the programs that open b at its extensions page here, or
// none when its program is not found (or there is no display to open it on).
func (c *browserConnect) openCmds(b chromeBrowser) []browserOpenCmd {
	p, ok := browserPrograms[b.Name]
	if !ok {
		return nil
	}
	// A shell reached over SSH: the person is not at this computer's
	// screen, so nothing is opened on it. (A reminal session watched from
	// another device has neither variable, and is offered as usual.)
	if c.d.getenv("SSH_CONNECTION") != "" || c.d.getenv("SSH_TTY") != "" {
		return nil
	}
	switch c.d.goos {
	case "darwin":
		// The folder first, so the browser ends up in front of it.
		return []browserOpenCmd{
			{argv: []string{"open", "-R", chromeExtensionDir(c.home)}, wait: true, optional: true},
			// The page is handed to the app as the address to open: that
			// reaches the extensions page whether or not the browser is
			// running. Passed as a program argument (`--args`) it opens
			// only a new-tab page on a cold start (seen with Chromium 160
			// and Chrome 156 on macOS 13.6).
			{argv: []string{"open", "-a", p.macApp, p.url()}, wait: true},
		}
	}
	// On Windows and Linux the browser is started with no address: a
	// chrome:// address on the command line is ignored there and the browser
	// opens a new-tab page, with a blank tab beside it when it was already
	// running (seen with Chrome 158 on Windows 11). On macOS, handing it to
	// the app as the address to open does land. So here the person is told
	// where to go in the window that opens (finish).
	if c.d.goos == "windows" {
		if p.win == "" {
			return nil
		}
		for _, root := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
			dir := c.d.getenv(root)
			if dir == "" {
				continue
			}
			if exe := strings.TrimRight(dir, `\`) + `\` + p.win; c.d.exists(exe) {
				return []browserOpenCmd{{argv: []string{exe}}}
			}
		}
		return nil
	}
	if c.d.getenv("DISPLAY") == "" && c.d.getenv("WAYLAND_DISPLAY") == "" {
		return nil // a terminal with no desktop: nowhere to open a browser
	}
	for _, name := range p.linux {
		if exe, err := c.d.lookPath(name); err == nil {
			return []browserOpenCmd{{argv: []string{exe}}}
		}
	}
	return nil
}

// pick is the browser to open: the one that connected last when it is among
// those set up, else the first that can be opened here.
func (c *browserConnect) pick() (chromeBrowser, []browserOpenCmd) {
	order := make([]chromeBrowser, 0, len(c.browsers))
	for _, b := range c.browsers {
		if c.first.Family != "" && browserPrograms[b.Name].family == c.first.Family {
			order = append(order, b)
		}
	}
	order = append(order, c.browsers...)
	for _, b := range order {
		if cmds := c.openCmds(b); len(cmds) > 0 {
			return b, cmds
		}
	}
	return c.browsers[0], nil
}

// readLines hands over what the person types, a line at a time, and closes
// when the terminal does. Closing stop ends it once its read returns; a read
// of a terminal nobody types into returns only with the process.
func (c *browserConnect) readLines() (<-chan string, chan<- struct{}) {
	lines, stop := make(chan string), make(chan struct{})
	c.readerDone = make(chan struct{})
	go func() {
		defer close(c.readerDone)
		defer close(lines)
		r := bufio.NewReader(c.d.in)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			select {
			case lines <- line:
			case <-stop:
				return
			}
		}
	}()
	return lines, stop
}

// finish is the end of a setup: the line for a browser already connected,
// or the offer to open the extensions page and the wait for the extension.
func (c *browserConnect) finish() {
	if !c.active {
		return
	}
	out := c.d.out
	if c.first.Connected {
		fmt.Fprintln(out)
		sayWrapped(out, "", "", client.BrowserFamilyName(c.first.Family)+" is already connected. Agents in your reminal sessions can use it now.")
		return
	}
	hup := c.d.hup
	if hup == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGHUP, syscall.SIGTERM)
		defer signal.Stop(ch)
		hup = ch
	}
	lines, stop := c.readLines()
	defer close(stop)

	b, cmds := c.pick()
	if len(cmds) > 0 {
		// Only a Mac's open lands on the extensions page (openCmds), so
		// only there is the page what is offered.
		pageOpens := c.d.goos == "darwin"
		if pageOpens {
			fmt.Fprintf(out, "\nOpen the extensions page in %s now? [Y/n] ", b.Name)
		} else {
			fmt.Fprintf(out, "\nOpen %s now? [Y/n] ", b.Name)
		}
		select {
		case line, ok := <-lines:
			if !ok {
				fmt.Fprintln(out)
				return // the terminal closed
			}
			if a := strings.ToLower(strings.TrimSpace(line)); a == "" || a == "y" || a == "yes" {
				failed := false
				for _, cmd := range cmds {
					if err := c.d.run(cmd); err != nil && !cmd.optional {
						failed = true
						sayWrapped(out, "", "", fmt.Sprintf("Could not open %s (%v): open it yourself and go to %s.", b.Name, err, browserPrograms[b.Name].url()))
					}
				}
				// Whether the page really opened cannot be seen from
				// here, so where to go is always said; where only the
				// browser was opened, it is the next thing to do.
				switch {
				case failed:
				case pageOpens:
					sayWrapped(out, "", "", fmt.Sprintf("If the extensions page did not open, go to %s in %s.", browserPrograms[b.Name].url(), b.Name))
				default:
					sayWrapped(out, "", "", fmt.Sprintf("In the %s window that opened, go to %s (type it in the address bar).", b.Name, browserPrograms[b.Name].url()))
				}
			}
		case <-hup:
			return
		}
	}

	last := c.d.status()
	if last.Connected {
		c.sayConnected(last)
		return
	}
	if !last.Daemon {
		fmt.Fprintln(out)
		sayWrapped(out, "", "", "Not waiting for the extension: reminal's daemon is not running on this computer. Starting a reminal session starts it (run "+noBreak("`reminal new`")+"); if it still is not running, run "+noBreak("`reminal daemon --install`")+". The extension connects by itself once it is loaded and the daemon is running.")
		return
	}
	if last.NoAnswer {
		fmt.Fprintln(out)
		c.sayOldDaemon()
		return
	}
	fmt.Fprintln(out)
	sayWrapped(out, "", "", "Waiting for the extension to connect, for up to 2 minutes. Press Enter to stop waiting.")
	timeout := time.NewTimer(c.d.wait)
	defer timeout.Stop()
	tick := time.NewTicker(c.d.poll)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if last = c.d.status(); last.Connected {
				c.sayConnected(last)
				return
			}
			if last.NoAnswer {
				c.sayOldDaemon()
				return
			}
		case _, ok := <-lines:
			if ok {
				c.sayNotYet(b, last)
			}
			return // a key, or the terminal closed
		case <-hup:
			return
		case <-timeout.C:
			c.sayNotYet(b, last)
			return
		}
	}
}

// sayWrapped prints a sentence wrapped on words to the width integrate
// prints at, as the rest of its output is (integrate_out.go).
func sayWrapped(w io.Writer, first, rest, text string) {
	for _, ln := range wrapWords(text, integrateWidth(), first, rest) {
		fmt.Fprintln(w, ln)
	}
}

func (c *browserConnect) sayConnected(s client.BrowserStatus) {
	sayWrapped(c.d.out, "", "", client.BrowserFamilyName(s.Family)+" connected. Agents in your reminal sessions can use it now.")
}

// sayOldDaemon is the line for a daemon from before it could be asked the
// browser's state (the one still running just after an upgrade): there is
// nothing to wait on, and no reason is given, since none is known. No
// command either: that daemon gives way to the new one by itself, within a
// minute of its program being replaced (watchBinaryAndExit).
func (c *browserConnect) sayOldDaemon() {
	sayWrapped(c.d.out, "", "", "Not waiting for the extension: the reminal running in the background on this computer is an older one that cannot say whether the browser is connected. It is replaced by the new one within a minute of an upgrade; the extension connects by itself once it is loaded, and running "+noBreak("`reminal integrate`")+" again then confirms it.")
}

// sayNotYet is the line for a wait that ended without a connection: the
// likely reason, and that nothing is lost by not waiting.
func (c *browserConnect) sayNotYet(b chromeBrowser, s client.BrowserStatus) {
	switch {
	case !s.Daemon:
		sayWrapped(c.d.out, "", "", "Not connected yet: reminal's daemon stopped running on this computer. The extension connects by itself once it is loaded and the daemon is running, so nothing is lost by not waiting.")
	case s.FolderTaken:
		sayWrapped(c.d.out, "", "", "Not connected yet: a folder named reminal-extension in your home folder is on this computer and is not reminal's. Move it away, then run "+noBreak("`reminal integrate`")+" again; nothing is lost by not waiting.")
	case s.FilesGone:
		sayWrapped(c.d.out, "", "", "Not connected yet: the files of reminal's browser extension are no longer on this computer. Run "+noBreak("`reminal integrate`")+" again to put them back; nothing is lost by not waiting.")
	case !s.Last.IsZero():
		sayWrapped(c.d.out, "", "", "Not connected yet: most likely no browser window is open, or the extension is switched off or no longer loaded (on "+client.BrowserExtensionsPage(s.Family)+": turn Developer mode on and switch reminal's extension on, press its reload button, or if it is not listed, Load unpacked the reminal-extension folder again). It connects by itself once that is put right, so nothing is lost by not waiting.")
	default:
		sayWrapped(c.d.out, "", "", fmt.Sprintf("Not connected yet: most likely the extension is not loaded in %s yet. It connects by itself once it is loaded, so nothing is lost by not waiting.", b.Name))
	}
}
