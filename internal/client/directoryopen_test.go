// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/session"
)

// The open lookup exists so the machines panel can open a forwarded app in one
// click. It hands back a live PIN, so these tests pin down exactly who gets one:
// an exposed port, asked for by id, and nothing else.
func TestApplyLocalOpen(t *testing.T) {
	isolateHome(t)
	pid := os.Getpid() // ReadAllActive prunes dead pids

	if err := session.WriteActive(session.Active{
		ID: "PORT1234", Kind: "port", Port: 3000, PID: pid,
		PIN: "246810", OpenURL: "https://port-port1234.reminal.app/",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteActive(session.Active{
		ID: "SHEL5678", Name: "work", PIN: "123456", Cwd: "/home/x", PID: pid,
	}); err != nil {
		t.Fatal(err)
	}
	// An exposed port whose link was never recorded — an older tunnel, or one
	// that died before it registered.
	if err := session.WriteActive(session.Active{
		ID: "NOURL999", Kind: "port", Port: 4000, PID: pid, PIN: "999999",
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("an exposed port hands back its link and gate PIN", func(t *testing.T) {
		var resp protocol.DirResponse
		applyLocalOpen(&resp, "PORT1234")
		if resp.OpenError != "" {
			t.Fatalf("unexpected error: %s", resp.OpenError)
		}
		if resp.OpenURL != "https://port-port1234.reminal.app/" {
			t.Errorf("OpenURL = %q", resp.OpenURL)
		}
		if resp.OpenPIN != "246810" {
			t.Errorf("OpenPIN = %q", resp.OpenPIN)
		}
	})

	t.Run("lowercase id still resolves", func(t *testing.T) {
		var resp protocol.DirResponse
		applyLocalOpen(&resp, "port1234")
		if resp.OpenURL == "" {
			t.Fatalf("lowercase id did not resolve: %s", resp.OpenError)
		}
	})

	t.Run("a shell session never hands back a PIN", func(t *testing.T) {
		var resp protocol.DirResponse
		applyLocalOpen(&resp, "SHEL5678")
		if resp.OpenPIN != "" || resp.OpenURL != "" {
			t.Fatalf("shell leaked url=%q pin=%q", resp.OpenURL, resp.OpenPIN)
		}
		if !strings.Contains(resp.OpenError, "not an exposed port") {
			t.Errorf("OpenError = %q", resp.OpenError)
		}
	})

	t.Run("a port with no recorded link is an error, not a bare PIN", func(t *testing.T) {
		var resp protocol.DirResponse
		applyLocalOpen(&resp, "NOURL999")
		if resp.OpenPIN != "" {
			t.Fatalf("handed back a PIN with no link to use it on: %q", resp.OpenPIN)
		}
		if !strings.Contains(resp.OpenError, "no public link") {
			t.Errorf("OpenError = %q", resp.OpenError)
		}
	})

	t.Run("an unknown id says so and leaks nothing", func(t *testing.T) {
		var resp protocol.DirResponse
		applyLocalOpen(&resp, "NOSUCH00")
		if resp.OpenURL != "" || resp.OpenPIN != "" {
			t.Fatalf("leaked url=%q pin=%q", resp.OpenURL, resp.OpenPIN)
		}
		if !strings.Contains(resp.OpenError, "no session") {
			t.Errorf("OpenError = %q", resp.OpenError)
		}
	})
}

// The whole point of answering per click is that the PIN stays out of the
// listing, which the viewer caches to IndexedDB. A plain query must carry no
// PIN even now that the response has a field able to hold one.
func TestPlainListingStillCarriesNoPIN(t *testing.T) {
	isolateHome(t)
	pid := os.Getpid()
	if err := session.WriteActive(session.Active{
		ID: "PORT1234", Kind: "port", Port: 3000, PID: pid,
		PIN: "246810", OpenURL: "https://port-port1234.reminal.app/",
	}); err != nil {
		t.Fatal(err)
	}

	resp := LocalDirectory()
	blob, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"246810", "open_pin"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("plain listing carried %q: %s", secret, blob)
		}
	}
	// Sanity: the port really is in that listing, so the check above is not
	// passing simply because nothing was there.
	if !strings.Contains(string(blob), "PORT1234") {
		t.Fatalf("port session missing from listing: %s", blob)
	}
}

// TestOpenOverTheDirectoryChannel drives the real wire path against a test
// relay: an owner asks for one port's link over the encrypted machine channel
// and gets the URL and gate PIN back, while the same listing still carries no
// PIN at all.
func TestOpenOverTheDirectoryChannel(t *testing.T) {
	isolateHome(t)
	startTestRelay(t)

	id, err := MyOwnerID()
	if err != nil {
		t.Fatalf("owner id: %v", err)
	}
	if _, _, err := AddOwner(id, "self"); err != nil {
		t.Fatalf("add owner: %v", err)
	}
	machinePub, err := MachinePub()
	if err != nil {
		t.Fatalf("machine pub: %v", err)
	}

	pid := os.Getpid()
	if err := session.WriteActive(session.Active{
		ID: "PORTWIRE", Kind: "port", Port: 8080, PID: pid,
		PIN: "864209", OpenURL: "https://port-portwire.reminal.app/",
	}); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	defer close(stop)
	go runDirectoryHost(stop, true, "test")

	deadline := time.Now().Add(6 * time.Second)
	for {
		resp, qerr := queryDirectory(machinePub, DirectoryTimeout, dirQueryReq{OpenID: "PORTWIRE"})
		if qerr == nil && (resp.OpenURL != "" || resp.OpenError != "") {
			if resp.OpenError != "" {
				t.Fatalf("host refused: %s", resp.OpenError)
			}
			if resp.OpenURL != "https://port-portwire.reminal.app/" {
				t.Errorf("OpenURL = %q", resp.OpenURL)
			}
			if resp.OpenPIN != "864209" {
				t.Errorf("OpenPIN = %q", resp.OpenPIN)
			}
			// And a plain listing over the same channel still carries nothing.
			plain, perr := QueryDirectory(machinePub, DirectoryTimeout)
			if perr != nil {
				t.Fatalf("plain query: %v", perr)
			}
			blob, _ := json.Marshal(plain)
			if strings.Contains(string(blob), "864209") {
				t.Fatalf("PIN leaked into the plain listing: %s", blob)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("host never answered the open lookup (last err: %v)", qerr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
