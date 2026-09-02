// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/crypto"
	"github.com/reminal/reminal/internal/protocol"
	"github.com/reminal/reminal/internal/relay"
	"github.com/reminal/reminal/internal/session"
)

// TestDirectoryRevokeSelfEndToEnd drives dir_revoke_self over a real relay and
// confirms the device stops being an owner afterward.
func TestDirectoryRevokeSelfEndToEnd(t *testing.T) {
	isolateHome(t)
	startTestRelay(t)
	id, err := MyOwnerID()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := AddOwner(id, "self"); err != nil {
		t.Fatal(err)
	}
	machinePub, _ := MachinePub()
	dk, _ := loadOrCreateDeviceKey()
	devicePub := dk.Public().(ed25519.PublicKey)
	if err := session.WriteActive(session.Active{ID: "ABCD2345", Name: "x", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	defer close(stop)
	go runDirectoryHost(stop, true)

	deadline := time.Now().Add(6 * time.Second)
	for {
		if _, e := QueryDirectory(machinePub, DirectoryTimeout); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("host never came up")
		}
		time.Sleep(100 * time.Millisecond)
	}

	dirID := crypto.DeriveDirectoryID(machinePub)
	d := *websocket.DefaultDialer
	d.HandshakeTimeout = 6 * time.Second
	conn, _, err := d.Dial(config.SessionWS(dirID, "viewer"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(6 * time.Second))
	if err := writeDir(conn, protocol.Message{Type: protocol.TypeAuth}); err != nil {
		t.Fatal(err)
	}
	if err := waitAuthOK(conn); err != nil {
		t.Fatal(err)
	}
	exHex, exID, _ := crypto.NewExID()
	eph, _ := crypto.NewEphemeralKey()
	ve := eph.PublicKey().Bytes()
	b64 := base64.StdEncoding.EncodeToString
	sig := crypto.SignOwner(dk, crypto.OwnerClientTranscript(dirID, ve, devicePub))
	writeDir(conn, protocol.Message{Type: protocol.TypeOwnerInit, ExID: exHex, Data: b64(ve), DevicePub: b64(devicePub), DeviceSig: b64(sig)})
	box, err := readOwnerResp(conn, dirID, exHex, exID, ve, devicePub, machinePub, eph)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	rsig := crypto.SignOwner(dk, crypto.RevokeSelfTranscript(machinePub, devicePub))
	req, _ := json.Marshal(map[string]string{"device_pub": b64(devicePub), "sig": b64(rsig), "req_id": "r1"})
	enc, _ := box.Encrypt(req)
	writeDir(conn, protocol.Message{Type: protocol.TypeDirRevokeSelf, Data: enc})
	for {
		var msg protocol.Message
		if err := readDir(conn, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != protocol.TypeDirRevokeSelf {
			continue
		}
		pt, _ := box.Decrypt(msg.Data)
		var r struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		json.Unmarshal(pt, &r)
		if !r.OK {
			t.Fatalf("revoke failed: %s", r.Error)
		}
		break
	}
	if ok, _ := IsOwner(devicePub); ok {
		t.Fatal("device should NOT be an owner after self-revoke")
	}
}

// TestDirectoryNewSessionRequiresOwnerKey guards the security gate: only a party
// that completed the owner handshake (and thus holds the channel key) can spawn
// a session. A request that doesn't decrypt — from anyone who merely knows the
// channel id, including the relay — must NOT spawn anything.
func TestDirectoryNewSessionRequiresOwnerKey(t *testing.T) {
	key, err := crypto.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := crypto.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	spawned := 0
	dh := &dirHost{
		box:        box,
		spawnLimit: newTokenBucket(8, 8),
		spawn: func(string, string) (*SpawnedSession, error) {
			spawned++
			return &SpawnedSession{ID: "X", PIN: "1"}, nil
		},
	}
	// No data → no spawn.
	dh.handleNewSession(protocol.Message{Type: protocol.TypeNewSession})
	// Junk that isn't valid ciphertext → no spawn.
	dh.handleNewSession(protocol.Message{Type: protocol.TypeNewSession, Data: base64.StdEncoding.EncodeToString([]byte("not encrypted"))})
	// Validly encrypted, but under a DIFFERENT key (a non-owner) → no spawn.
	otherBox, _ := crypto.NewBox(mustSessionKey(t))
	badEnc, _ := otherBox.Encrypt([]byte(`{"name":"x"}`))
	dh.handleNewSession(protocol.Message{Type: protocol.TypeNewSession, Data: badEnc})

	if spawned != 0 {
		t.Fatalf("spawned %d times for unauthenticated requests — must be 0", spawned)
	}
}

// TestDirectoryNewSessionPassesCwd is the duplicate-into-folder path: an
// authenticated owner request carries cwd, and Spawn is called with it.
func TestDirectoryNewSessionPassesCwd(t *testing.T) {
	key, err := crypto.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := crypto.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	var gotName, gotCwd string
	dh := &dirHost{
		box:        box,
		spawnLimit: newTokenBucket(8, 8),
		spawn: func(name, cwd string) (*SpawnedSession, error) {
			gotName, gotCwd = name, cwd
			return &SpawnedSession{ID: "X", PIN: "1"}, nil
		},
	}
	pt, _ := json.Marshal(map[string]string{"name": "dup", "cwd": "/tmp", "req_id": "r1"})
	enc, err := box.Encrypt(pt)
	if err != nil {
		t.Fatal(err)
	}
	dh.handleNewSession(protocol.Message{Type: protocol.TypeNewSession, Data: enc})
	if gotName != "dup" || gotCwd != "/tmp" {
		t.Fatalf("spawn(%q, %q), want dup /tmp", gotName, gotCwd)
	}
}

func mustSessionKey(t *testing.T) []byte {
	t.Helper()
	k, err := crypto.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestQueryDirectoryDialTimeout ensures the timeout bounds the DIAL, not just
// the reads — an unreachable machine must fail in ~timeout, not the dialer's
// 45s default (which would stall `reminal machines`).
func TestQueryDirectoryDialTimeout(t *testing.T) {
	isolateHome(t)
	// A listener that accepts TCP but never completes the WebSocket handshake.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go io.Copy(io.Discard, c) // read the request, never reply → handshake hangs
		}
	}()
	t.Setenv("REMINAL_RELAY", "ws://"+ln.Addr().String())

	machinePub, err := MachinePub()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, qerr := QueryDirectory(machinePub, 500*time.Millisecond); qerr == nil {
		t.Fatal("expected an error dialing a non-responding relay")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("dial not bounded by timeout: took %v (want ~500ms)", elapsed)
	}
}

// startTestRelay spins up an in-process relay and points config.RelayWS at it.
func startTestRelay(t *testing.T) {
	t.Helper()
	srv := relay.NewServer()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 2 {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		srv.HandleSessionWS(w, r, parts[0], parts[1])
	}))
	t.Cleanup(ts.Close)
	t.Setenv("REMINAL_RELAY", "ws"+strings.TrimPrefix(ts.URL, "http"))
}

// TestDirectoryEndToEnd exercises the whole owner-derived directory path against
// a real relay: the host registers + serves, the owner query proves ownership
// with the signed handshake, and the encrypted session list comes back intact.
func TestDirectoryEndToEnd(t *testing.T) {
	isolateHome(t)
	startTestRelay(t)

	// Enroll THIS device as an owner so the host's IsOwner check passes.
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

	// Seed a shell session and a port forward into the local registry. PID must
	// be a live process (ReadAllActive prunes dead ones), so use the test's.
	pid := os.Getpid()
	if err := session.WriteActive(session.Active{ID: "ABCD2345", Name: "work", PIN: "123456", Cwd: "/home/x", PID: pid}); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteActive(session.Active{ID: "PQRS6789", Kind: "port", Port: 3000, PID: pid}); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	defer close(stop)
	go runDirectoryHost(stop, true)

	// Poll until the host has won the channel and answers.
	deadline := time.Now().Add(6 * time.Second)
	var got int
	var host string
	for {
		r, qerr := QueryDirectory(machinePub, DirectoryTimeout)
		if qerr == nil {
			got = len(r.Sessions)
			host = r.Hostname
			// The seeded PIN ("123456") must NEVER cross the directory channel —
			// owners reach sessions without it. DirSession has no PIN field so this
			// holds structurally today; assert on the serialized response so a
			// future field addition that leaks it fails loudly here.
			if blob, _ := json.Marshal(r); strings.Contains(string(blob), "123456") {
				t.Fatalf("PIN leaked onto the directory channel: %s", blob)
			}
			byID := map[string]bool{}
			for _, s := range r.Sessions {
				byID[s.ID] = true
			}
			if !byID["ABCD2345"] || !byID["PQRS6789"] {
				t.Fatalf("expected both seeded sessions, got %+v", r.Sessions)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("directory query never succeeded: %v", qerr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got != 2 {
		t.Fatalf("want 2 sessions, got %d", got)
	}
	if host == "" {
		t.Log("hostname empty (os.Hostname failed?) — not fatal")
	}
}

// TestDirectoryConcurrentQueries hammers one host with simultaneous queries.
// Because the host broadcasts each own_resp to every viewer, a query must match
// the reply to its OWN ex_id — otherwise concurrent owners cross-talk and fail.
func TestDirectoryConcurrentQueries(t *testing.T) {
	isolateHome(t)
	startTestRelay(t)

	id, err := MyOwnerID()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := AddOwner(id, "self"); err != nil {
		t.Fatal(err)
	}
	machinePub, err := MachinePub()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.WriteActive(session.Active{ID: "ABCD2345", Name: "work", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	defer close(stop)
	go runDirectoryHost(stop, true)

	// Wait for the host to come up.
	deadline := time.Now().Add(6 * time.Second)
	for {
		if _, qerr := QueryDirectory(machinePub, DirectoryTimeout); qerr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("host never came up")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Fire many queries at once; every one must succeed and see the session.
	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			resp, qerr := QueryDirectory(machinePub, DirectoryTimeout)
			if qerr != nil {
				errs <- qerr
				return
			}
			if len(resp.Sessions) != 1 || resp.Sessions[0].ID != "ABCD2345" {
				errs <- fmt.Errorf("unexpected sessions: %+v", resp.Sessions)
				return
			}
			errs <- nil
		}()
	}
	for i := 0; i < n; i++ {
		if e := <-errs; e != nil {
			t.Fatalf("concurrent query %d failed: %v", i, e)
		}
	}
}

// TestDirectoryRejectsNonOwner confirms that once a device is revoked it can no
// longer read the session list, even though it can still derive the channel id.
func TestDirectoryRejectsNonOwner(t *testing.T) {
	isolateHome(t)
	startTestRelay(t)

	id, err := MyOwnerID()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := AddOwner(id, "self"); err != nil {
		t.Fatal(err)
	}
	machinePub, err := MachinePub()
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	defer close(stop)
	go runDirectoryHost(stop, true)

	// While enrolled, the query must eventually succeed (host is up).
	deadline := time.Now().Add(6 * time.Second)
	for {
		if _, qerr := QueryDirectory(machinePub, DirectoryTimeout); qerr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owner query never succeeded while enrolled")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Revoke this device. IsOwner is re-checked live on every own_init, so the
	// handshake now gets no reply and the query fails.
	if _, _, err := RemoveOwner(id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, qerr := QueryDirectory(machinePub, 2*time.Second); qerr == nil {
		t.Fatal("a revoked device should not be able to read the directory")
	}
}

func TestRenameLocalSession(t *testing.T) {
	isolateHome(t)
	if err := renameLocalSession("NOSUCHID", "x"); err == nil {
		t.Error("expected error renaming a non-existent session")
	}
	if err := renameLocalSession("ANY", "   "); err == nil {
		t.Error("expected error for an empty name")
	}
}

func TestKillLocalSession(t *testing.T) {
	isolateHome(t)
	if err := killLocalSession("NOSUCHID"); err == nil {
		t.Error("expected error killing a non-existent session")
	}
	// Register a session backed by a real throwaway child, then kill it.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteActive(session.Active{ID: "KILLME12", PID: cmd.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	if err := killLocalSession("KILLME12"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done: // SIGTERM'd → exited
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child was not killed by killLocalSession")
	}
}
