package client

import (
	"bytes"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reminal/reminal/internal/protocol"
)

// memConn is an in-memory frameConn: frames sent on one end appear on the
// other's recv. Lets us drive a full source<->paste handshake with no relay.
type memConn struct {
	out chan protocol.Message
	in  chan protocol.Message
}

func (m *memConn) send(msg protocol.Message) error {
	m.out <- msg
	return nil
}

func (m *memConn) recv() (protocol.Message, error) {
	msg, ok := <-m.in
	if !ok {
		return protocol.Message{}, io.EOF
	}
	return msg, nil
}

func (m *memConn) setReadDeadline(time.Time) error { return nil }

func newMemPair() (*memConn, *memConn) {
	a := make(chan protocol.Message, 256)
	b := make(chan protocol.Message, 256)
	return &memConn{out: a, in: b}, &memConn{out: b, in: a}
}

func TestRendezvousRoundTrip(t *testing.T) {
	const code = "4821-739"
	rng := rand.New(rand.NewSource(7))
	sizes := []int{0, 1, downloadChunkBytes - 1, downloadChunkBytes, downloadChunkBytes + 1, 3*downloadChunkBytes + 99}

	for _, size := range sizes {
		data := make([]byte, size)
		rng.Read(data)

		srcDir := t.TempDir()
		srcFile := filepath.Join(srcDir, "payload.bin")
		if err := os.WriteFile(srcFile, data, 0o644); err != nil {
			t.Fatalf("size %d: write source: %v", size, err)
		}
		dstDir := t.TempDir()

		srcConn, pasteConn := newMemPair()
		srcErr := make(chan error, 1)
		type pres struct {
			path string
			err  error
		}
		pasteCh := make(chan pres, 1)
		go func() { srcErr <- runSource(srcConn, code, srcFile) }()
		go func() {
			p, e := runPaste(pasteConn, code, dstDir)
			pasteCh <- pres{p, e}
		}()

		// Source returns once the paste acks delivery.
		if err := <-srcErr; err != nil {
			t.Fatalf("size %d: source: %v", size, err)
		}
		// Paste blocks until the source closes; the relay does that in
		// production, so simulate it by closing the source→paste channel.
		close(srcConn.out)
		res := <-pasteCh
		if res.err != nil {
			t.Fatalf("size %d: paste: %v", size, res.err)
		}
		gotPath := res.path

		got, err := os.ReadFile(gotPath)
		if err != nil {
			t.Fatalf("size %d: read result: %v", size, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("size %d: bytes differ (got %d, want %d)", size, len(got), len(data))
		}
		if filepath.Base(gotPath) != "payload.bin" {
			t.Fatalf("size %d: wrong dest name %q", size, gotPath)
		}
	}
}

// A wrong code must fail the paste's unwrap, return errWrongCode, and leave
// NO file behind — the source must not have streamed any bytes.
func TestRendezvousWrongCode(t *testing.T) {
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "secret.bin")
	if err := os.WriteFile(srcFile, []byte("top secret contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	dstDir := t.TempDir()

	srcConn, pasteConn := newMemPair()
	srcErr := make(chan error, 1)
	go func() { srcErr <- runSource(srcConn, "RIGHT-CODE", srcFile) }()

	_, err := runPaste(pasteConn, "WRONG-CODE", dstDir)
	if err != errWrongCode {
		t.Fatalf("paste error = %v, want errWrongCode", err)
	}
	// Source is blocked awaiting a confirm that never comes; closing the
	// paste->source channel mimics the paste disconnecting. Source must end
	// in error, never having written/streamed the file.
	close(pasteConn.out)
	if serr := <-srcErr; serr == nil {
		t.Fatal("source returned nil error on wrong-code transfer")
	}

	entries, err := os.ReadDir(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrong code leaked %d file(s) into dest: %v", len(entries), entries)
	}
}
