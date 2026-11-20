// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// `reminal browser-host`: Chrome's native messaging host for reminal's
// extension (extensions/chrome). Chrome starts it when the extension calls
// connectNative("app.reminal.browser"), and talks to it over stdin/stdout in
// its own framing: a 4-byte length in the machine's byte order, then that
// many bytes of JSON. The host joins the daemon's browser hub
// (client.ServeBrowser) as the browser, and carries messages both ways:
// requests from sessions to the extension, its answers back. It ends when
// Chrome closes its stdin (the extension's port closed), or the hub does.

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"reminal/internal/client"
)

// The limits Chrome sets: at most 1 MB from a host to the extension, at
// most 64 MB the other way.
const (
	nmMaxToExtension   = 1 << 20
	nmMaxFromExtension = 64 << 20
)

// browserExtensionOrigin is the origin of reminal's extension: its id is
// fixed by the "key" in extensions/chrome/manifest.json.
const browserExtensionOrigin = "chrome-extension://" + browserExtensionID + "/"

func runBrowserHost(args []string) error {
	// Chrome names the extension asking as the first argument; only
	// reminal's own is served (the host manifest allows no other, too).
	if len(args) > 0 && strings.HasPrefix(args[0], "chrome-extension://") && args[0] != browserExtensionOrigin {
		return fmt.Errorf("not reminal's extension: %s", args[0])
	}
	sock, err := client.BrowserSockPath()
	if err != nil {
		return err
	}
	conn, err := dialHub(sock)
	if err != nil {
		// Said to the extension too, so its popup can show it.
		_ = writeNativeMessage(os.Stdout, map[string]any{"t": "status", "error": "reminal's daemon is not running on this computer"})
		return err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"t":"host"}` + "\n")); err != nil {
		return err
	}
	_ = writeNativeMessage(os.Stdout, map[string]any{"t": "status", "ok": true})
	return relayNative(os.Stdin, os.Stdout, conn)
}

func dialHub(sock string) (net.Conn, error) {
	var last error
	for i := 0; i < 20; i++ { // the daemon may be starting with the login
		c, err := net.DialTimeout("unix", sock, time.Second)
		if err == nil {
			return c, nil
		}
		last = err
		time.Sleep(250 * time.Millisecond)
	}
	return nil, last
}

// relayNative carries messages between Chrome (in, out: native framing) and
// the hub (hub: lines of JSON) until either side ends.
func relayNative(in io.Reader, out io.Writer, hub net.Conn) error {
	done := make(chan error, 2)
	go func() { // the extension → the hub
		for {
			msg, err := readNativeMessage(in)
			if err != nil {
				done <- err
				return
			}
			if _, err := hub.Write(append(msg, '\n')); err != nil {
				done <- err
				return
			}
		}
	}()
	go func() { // the hub → the extension
		r := bufio.NewReaderSize(hub, 64<<10)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 1 {
				var v json.RawMessage = line[:len(line)-1]
				if werr := writeNativeRaw(out, v); werr != nil {
					done <- werr
					return
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	err := <-done
	if errors.Is(err, io.EOF) {
		return nil // Chrome closed the port, or the hub went away
	}
	return err
}

// readNativeMessage reads one message in Chrome's framing.
func readNativeMessage(r io.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.NativeEndian, &n); err != nil {
		return nil, err
	}
	if n > nmMaxFromExtension {
		return nil, fmt.Errorf("message of %d bytes is too long", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	if !json.Valid(buf) {
		return nil, errors.New("not JSON")
	}
	return buf, nil
}

func writeNativeMessage(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeNativeRaw(w, b)
}

func writeNativeRaw(w io.Writer, b []byte) error {
	if len(b) > nmMaxToExtension {
		return fmt.Errorf("message of %d bytes is too long for Chrome", len(b))
	}
	var hdr [4]byte
	binary.NativeEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(append(hdr[:], b...)); err != nil {
		return err
	}
	return nil
}
