package client

import (
	"io"
	"os"
	"strings"

	"github.com/mdp/qrterminal/v3"
	"golang.org/x/term"
)

// A join banner and its QR are what people redirect to a file to share
// (`reminal new > shareme.txt`), so when stdout is not a terminal they are
// written as plain ASCII. Windows PowerShell 5.1 decodes a program's
// redirected output with the console's legacy code page, which garbles every
// non-ASCII byte (issue #198): the half-block QR stops scanning and "—"
// becomes "ΓÇö". ASCII comes through any shell and any encoding unchanged.

func stdoutIsTerminal() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// bannerOut is where a join banner goes: stdout, folded to ASCII when stdout
// is a file or a pipe.
func bannerOut() io.Writer {
	if stdoutIsTerminal() {
		return os.Stdout
	}
	return asciiWriter{os.Stdout}
}

// asciiFold covers the typography the banners use. Anything else non-ASCII
// (a session name, a path) is the user's own text and is left as it is.
var asciiFold = strings.NewReplacer("—", "-", "·", "|", "…", "...", "→", "->", "✓", "ok")

type asciiWriter struct{ w io.Writer }

func (a asciiWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(a.w, asciiFold.Replace(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// printJoinQR draws a QR for url, styled for where stdout goes.
func printJoinQR(w io.Writer, url string) { renderQR(w, url, stdoutIsTerminal()) }

// renderQR draws a QR for url. On a terminal it is half blocks, compact and
// the same from every command (issue #85). Anywhere else it is "##" for each
// dark module and spaces for light: dark on light, as a printed QR is, in any
// editor or viewer.
func renderQR(w io.Writer, url string, terminal bool) {
	if terminal {
		qrterminal.GenerateHalfBlock(url, qrterminal.L, w)
		return
	}
	qrterminal.GenerateWithConfig(url, qrterminal.Config{
		Level:     qrterminal.L,
		Writer:    w,
		BlackChar: "##",
		WhiteChar: "  ",
		QuietZone: qrterminal.QUIET_ZONE,
	})
}
