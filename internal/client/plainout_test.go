package client

import (
	"bytes"
	"strings"
	"testing"
)

// Issue #198: a QR written to a file must be plain ASCII, or Windows
// PowerShell 5.1 garbles it on the way into the file.
func TestRedirectedQRIsPlainASCII(t *testing.T) {
	var b bytes.Buffer
	renderQR(&b, "https://live.reminal.app/?s=ABCD1234#p=123456", false)
	if b.Len() == 0 {
		t.Fatal("no QR written")
	}
	for i, c := range b.Bytes() {
		if c > 0x7e || (c < 0x20 && c != '\n') {
			t.Fatalf("byte %d is %#x; a redirected QR must be printable ASCII", i, c)
		}
	}
	if !strings.Contains(b.String(), "##") {
		t.Fatal("no dark modules drawn")
	}
}

// Control for the test above: on a terminal the QR stays half blocks, so the
// ASCII check is not passing because nothing non-ASCII is ever drawn.
func TestTerminalQRIsHalfBlocks(t *testing.T) {
	var b bytes.Buffer
	renderQR(&b, "https://live.reminal.app/?s=ABCD1234#p=123456", true)
	if !strings.ContainsAny(b.String(), "▀▄█") {
		t.Fatal("terminal QR should use half blocks")
	}
}

func TestASCIIWriterFoldsBannerTypography(t *testing.T) {
	var b bytes.Buffer
	n, err := asciiWriter{&b}.Write([]byte("  reminal — new background session · v1 · ID…"))
	if err != nil || n != len("  reminal — new background session · v1 · ID…") {
		t.Fatalf("Write = %d, %v; want the input length and no error", n, err)
	}
	if got, want := b.String(), "  reminal - new background session | v1 | ID..."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
