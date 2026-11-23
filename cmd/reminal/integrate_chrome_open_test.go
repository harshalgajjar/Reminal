// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// macBrowsers: each browser of the table, its application's name, and its
// extensions page.
var macBrowsers = []struct{ name, app, page string }{
	{"Google Chrome", "Google Chrome", "chrome://extensions"},
	{"Google Chrome Beta", "Google Chrome Beta", "chrome://extensions"},
	{"Chromium", "Chromium", "chrome://extensions"},
	{"Microsoft Edge", "Microsoft Edge", "edge://extensions"},
	{"Brave", "Brave Browser", "brave://extensions"},
	{"Arc", "Arc", "chrome://extensions"},
}

// On a Mac, after a yes, the folder is shown in Finder and then the browser
// is handed its page as the address to open, and nothing else is started:
// that form reaches the page whether or not the browser is running.
func TestBrowserOpenOnAMacIsTheFolderThenThePage(t *testing.T) {
	ext := chromeExtensionDir("/home/u")
	if len(macBrowsers) != len(browserPrograms) {
		t.Fatalf("%d browsers here, %d in the table", len(macBrowsers), len(browserPrograms))
	}
	for _, b := range macBrowsers {
		r := newConnectRig(t, "darwin", hubNever)
		r.d.wait = 20 * time.Millisecond
		bc := newBrowserConnectWith(r.d, "/home/u", []chromeBrowser{{Name: b.name}}, false, false)
		r.typeLine("y")
		r.finish(bc)
		want := []string{"open -R " + ext, "open -a " + b.app + " " + b.page}
		if got := r.commands(); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: ran %q, want %q", b.name, got, want)
		}
		line := "If the extensions page did not open, go to " + b.page + " in " + b.name + ".\n"
		if out := r.out.String(); !strings.Contains(out, "? [Y/n] "+line) || strings.Contains(out, "Could not open") {
			t.Errorf("%s: after a yes:\n%s", b.name, out)
		}
	}
}

// After a yes, where to go is said on every system, with the browser's own
// page: whether the page really opened is not seen from here. After a no
// nothing is said of it.
func TestBrowserConnectSaysWhereTheExtensionsPageIs(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, b := range macBrowsers {
			if goos != "darwin" && b.name == "Arc" {
				continue // Arc is opened on a Mac only
			}
			// On a Mac the page is what opens; on Linux and Windows only
			// the browser does, with no address given to it, and the line
			// says where to go in its window.
			offer := "\nOpen the extensions page in " + b.name + " now? [Y/n] "
			line := "If the extensions page did not open, go to " + b.page + " in " + b.name + "."
			other := "window that opened"
			if goos != "darwin" {
				offer = "\nOpen " + b.name + " now? [Y/n] "
				line = "In the " + b.name + " window that opened, go to " + b.page + " (type it in the address bar)."
				other = "If the extensions page did not open"
			}
			for _, answer := range []string{"", "y", "yes", "n"} {
				r := newConnectRig(t, goos, hubNever)
				r.d.wait = 20 * time.Millisecond
				bc := newBrowserConnectWith(r.d, "/home/u", []chromeBrowser{{Name: b.name}}, false, false)
				r.typeLine(answer)
				r.finish(bc)
				out := r.out.String()
				if !strings.HasPrefix(out, offer) || strings.Count(out, "now? [Y/n]") != 1 {
					t.Fatalf("%s, %s: the offer:\n%s", goos, b.name, out)
				}
				if said := strings.Contains(out, offer+line+"\n"); said != (answer != "n") {
					t.Errorf("%s, %s, answer %q: the line said: %v\n%s", goos, b.name, answer, said, out)
				}
				if strings.Contains(out, other) || strings.Contains(out, "Could not open") {
					t.Errorf("%s, %s, answer %q: said another system's line:\n%s", goos, b.name, answer, out)
				}
				if answer == "n" && (len(r.commands()) != 0 || strings.Contains(out, "go to ")) {
					t.Errorf("%s, %s: after a no, ran %v:\n%s", goos, b.name, r.commands(), out)
				}
				if answer != "n" && goos != "darwin" {
					// The browser's own program and nothing after it.
					if len(r.ran) != 1 || len(r.ran[0]) != 1 || strings.Contains(r.ran[0][0], "://") {
						t.Errorf("%s, %s: started %q", goos, b.name, r.ran)
					}
				}
			}
		}
	}
}

// The line is not said where nothing is offered: with -y, off a terminal,
// and over SSH.
func TestBrowserConnectSaysNoPageWhereNothingIsOffered(t *testing.T) {
	for _, c := range []struct {
		name      string
		tty       bool
		assumeYes bool
		ssh       bool
	}{
		{"-y", true, true, false},
		{"off a terminal", false, false, false},
		{"over SSH", true, false, true},
	} {
		for _, goos := range []string{"darwin", "linux", "windows"} {
			r := newConnectRig(t, goos, hubNever)
			r.d.wait = 20 * time.Millisecond
			r.d.tty = func() bool { return c.tty }
			if c.ssh {
				was := r.d.getenv
				r.d.getenv = func(k string) string {
					if k == "SSH_CONNECTION" {
						return "10.0.0.1 1 10.0.0.2 22"
					}
					return was(k)
				}
			}
			bc := newBrowserConnectWith(r.d, "/home/u", chromeOnly, false, c.assumeYes)
			r.typeLine("y")
			r.finish(bc)
			if out := r.out.String(); strings.Contains(out, "now? [Y/n]") || strings.Contains(out, "If the extensions page did not open") || strings.Contains(out, "window that opened") || len(r.commands()) != 0 {
				t.Errorf("%s, %s: said %q, ran %v", c.name, goos, out, r.commands())
			}
		}
	}
}

// When the browser could not be opened, that line alone says where to go;
// the Finder reveal failing is not worth a line and does not count.
func TestBrowserConnectSaysOneLineWhenTheBrowserDidNotOpen(t *testing.T) {
	r := newConnectRig(t, "darwin", hubNever)
	r.d.wait = 20 * time.Millisecond
	r.runFn = func(argv []string) error {
		if argv[0] == "open" && argv[1] == "-a" {
			return errors.New("exit status 1")
		}
		return nil
	}
	bc := newBrowserConnectWith(r.d, "/home/u", []chromeBrowser{{Name: "Brave"}}, false, false)
	r.typeLine("y")
	r.finish(bc)
	out := r.out.String()
	if !strings.Contains(out, "Could not open Brave (exit status 1): open it yourself and go to brave://extensions.\n") || strings.Contains(out, "If the extensions page did not open") {
		t.Errorf("the browser did not open:\n%s", out)
	}

	r = newConnectRig(t, "darwin", hubNever)
	r.d.wait = 20 * time.Millisecond
	r.runFn = func(argv []string) error {
		if argv[0] == "open" && argv[1] == "-R" {
			return errors.New("exit status 1")
		}
		return nil
	}
	bc = newBrowserConnectWith(r.d, "/home/u", []chromeBrowser{{Name: "Brave"}}, false, false)
	r.typeLine("y")
	r.finish(bc)
	out = r.out.String()
	if strings.Contains(out, "Could not open") || !strings.Contains(out, "If the extensions page did not open, go to brave://extensions in Brave.\n") {
		t.Errorf("only the folder was not shown:\n%s", out)
	}
}

// The line fits a narrow terminal, a Mac's and the one of Linux and Windows.
func TestBrowserConnectPageLineFitsANarrowTerminal(t *testing.T) {
	for goos, starts := range map[string]string{
		"darwin":  "If the extensions page did not open,",
		"linux":   "In the Google Chrome Beta window that",
		"windows": "In the Google Chrome Beta window that",
	} {
		r := newConnectRig(t, goos, hubNever)
		r.d.wait = 20 * time.Millisecond
		integrateWidth = func() int { return 40 }
		bc := newBrowserConnectWith(r.d, "/home/u", []chromeBrowser{{Name: "Google Chrome Beta"}}, false, false)
		r.typeLine("y")
		r.finish(bc)
		out := r.out.String()
		if !strings.Contains(out, starts) || !strings.Contains(out, "chrome://extensions") {
			t.Fatalf("%s: the line was not said:\n%s", goos, out)
		}
		if goos != "darwin" && !strings.Contains(strings.Join(strings.Fields(out), " "), "In the Google Chrome Beta window that opened, go to chrome://extensions (type it in the address bar).") {
			t.Errorf("%s: the line, wrapped:\n%s", goos, out)
		}
		for _, ln := range strings.Split(out, "\n") {
			if strings.HasPrefix(ln, "Open ") {
				// The question's own line ends where the answer is typed,
				// and what follows it on that line is this test's, not a
				// terminal's.
				_, ln, _ = strings.Cut(ln, "? [Y/n] ")
			}
			if len([]rune(ln)) > 40 {
				t.Errorf("%s: wider than 40 columns: %q", goos, ln)
			}
		}
	}
}

// A browser that could not be started, on Linux and on Windows: that line
// alone says where to go.
func TestBrowserConnectSaysOneLineWhenTheBrowserDidNotStart(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		r := newConnectRig(t, goos, hubNever)
		r.d.wait = 20 * time.Millisecond
		r.runErr = errors.New("no such program")
		bc := newBrowserConnectWith(r.d, "/home/u", []chromeBrowser{{Name: "Brave"}}, false, false)
		r.typeLine("y")
		r.finish(bc)
		out := r.out.String()
		if !strings.HasPrefix(out, "\nOpen Brave now? [Y/n] Could not open Brave (no such program): open it yourself and go to brave://extensions.\n") || strings.Contains(out, "window that opened") || strings.Contains(out, "If the extensions page did not open") {
			t.Errorf("%s: the browser did not start:\n%s", goos, out)
		}
	}
}
