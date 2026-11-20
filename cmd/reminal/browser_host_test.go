// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	chromeext "reminal/extensions/chrome"
)

// Chrome's framing: a 4-byte length in the machine's order, then the JSON.
func TestNativeMessagesRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := writeNativeMessage(&buf, map[string]any{"id": 3, "ok": true}); err != nil {
		t.Fatal(err)
	}
	n := binary.NativeEndian.Uint32(buf.Bytes()[:4])
	if int(n) != buf.Len()-4 {
		t.Fatalf("length %d for %d bytes", n, buf.Len()-4)
	}
	got, err := readNativeMessage(&buf)
	if err != nil || string(got) != `{"id":3,"ok":true}` {
		t.Fatalf("%s, %v", got, err)
	}
	// Over Chrome's limit to the extension: not sent.
	if err := writeNativeRaw(io.Discard, make([]byte, nmMaxToExtension+1)); err == nil {
		t.Error("a message over 1 MB went to the extension")
	}
	// Not JSON from the extension: refused.
	var bad bytes.Buffer
	_ = binary.Write(&bad, binary.NativeEndian, uint32(3))
	bad.WriteString("abc")
	if _, err := readNativeMessage(&bad); err == nil {
		t.Error("not JSON was read")
	}
}

// The host carries messages both ways: from Chrome to the hub as lines, and
// lines from the hub to Chrome in its framing.
func TestTheHostRelaysBothWays(t *testing.T) {
	hubSide, hostSide := net.Pipe()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- relayNative(inR, outW, hostSide) }()

	go func() { _ = writeNativeMessage(inW, map[string]any{"id": 7, "ok": true}) }()
	line, err := bufio.NewReader(hubSide).ReadString('\n')
	if err != nil || line != `{"id":7,"ok":true}`+"\n" {
		t.Fatalf("the hub got %q, %v", line, err)
	}
	go func() { _, _ = hubSide.Write([]byte(`{"t":"req","id":1,"tool":"navigate"}` + "\n")) }()
	msg, err := readNativeMessage(outR)
	if err != nil || string(msg) != `{"t":"req","id":1,"tool":"navigate"}` {
		t.Fatalf("Chrome got %s, %v", msg, err)
	}
	inW.Close() // Chrome closed the port
	if err := <-done; err != nil {
		t.Fatalf("relay ended with %v", err)
	}
}

// The extension's id is the one its manifest's key gives (the host
// manifest allows only it): the first 16 bytes of the key's SHA-256, each
// hex digit as a letter a to p.
func TestTheExtensionIDIsItsKeys(t *testing.T) {
	raw, err := fs.ReadFile(chromeext.Files, "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Key         string   `json:"key"`
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	der, err := base64.StdEncoding.DecodeString(m.Key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	id := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return 'a' + (r - '0')
		}
		return 'k' + (r - 'a')
	}, hex.EncodeToString(sum[:16]))
	if id != browserExtensionID {
		t.Fatalf("the key gives %s, the host allows %s", id, browserExtensionID)
	}
	if !strings.Contains(strings.Join(m.Permissions, " "), "nativeMessaging") {
		t.Error("no nativeMessaging permission")
	}
}

// `reminal integrate chrome`: the host manifest for each browser of Chrome's
// family that is here (and none for one that is not), naming this reminal and
// only reminal's extension; the extension's files to load; --remove takes
// away only reminal's own manifests, and the files.
func TestIntegrateChromeWritesAndRemovesItsOwn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not on Windows yet")
	}
	home := t.TempDir()
	exe := "/usr/local/bin/reminal"
	table := chromeBrowserTable()
	chrome, chromium := table[0], table[2]
	for _, b := range []chromeBrowser{chrome, chromium} {
		if err := os.MkdirAll(filepath.Join(home, b.Dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Someone else's host beside ours, in Chromium's folder.
	theirs := filepath.Join(home, chromium.Dir, "NativeMessagingHosts", "com.example.other.json")
	_ = os.MkdirAll(filepath.Dir(theirs), 0o755)
	_ = os.WriteFile(theirs, []byte(`{"name":"com.example.other"}`), 0o644)

	if err := integrateChrome(home, exe, false, false, true, false); err != nil {
		t.Fatal(err)
	}
	for _, b := range table {
		raw, err := os.ReadFile(chromeHostManifestPath(home, b))
		here := b == chrome || b == chromium
		if here != (err == nil) {
			t.Errorf("%s: manifest there %v, browser there %v", b.Name, err == nil, here)
			continue
		}
		if !here {
			continue
		}
		var m struct {
			Name, Path, Type string
			Allowed          []string `json:"allowed_origins"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if m.Name != browserHostName || m.Path != exe || m.Type != "stdio" || len(m.Allowed) != 1 || m.Allowed[0] != "chrome-extension://"+browserExtensionID+"/" {
			t.Errorf("%s: %s", b.Name, raw)
		}
	}
	if _, err := os.Stat(filepath.Join(chromeExtensionDir(home), "manifest.json")); err != nil {
		t.Errorf("the extension was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(chromeExtensionDir(home), "icons", "128.png")); err != nil {
		t.Errorf("its icons were not written: %v", err)
	}

	if err := integrateChrome(home, exe, true, false, true, false); err != nil {
		t.Fatal(err)
	}
	for _, b := range []chromeBrowser{chrome, chromium} {
		if _, err := os.Stat(chromeHostManifestPath(home, b)); !os.IsNotExist(err) {
			t.Errorf("%s: still there after --remove", b.Name)
		}
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Errorf("someone else's host was removed: %v", err)
	}
	if _, err := os.Stat(chromeExtensionDir(home)); !os.IsNotExist(err) {
		t.Errorf("the extension's files are still there")
	}
}

// With no browser of Chrome's family here, it says so and writes nothing.
func TestIntegrateChromeWithNoBrowser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not on Windows yet")
	}
	home := t.TempDir()
	if err := integrateChrome(home, "/x/reminal", false, false, true, false); err == nil || !strings.Contains(err.Error(), "no Chrome") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(chromeExtensionDir(home)); !os.IsNotExist(err) {
		t.Error("wrote the extension anyway")
	}
}

// The browser tools are listed, each with the browser_ prefix, and say they
// ask the person first.
func TestBrowserToolsAreListed(t *testing.T) {
	want := map[string]bool{}
	for _, n := range []string{"tabs_context", "tabs_create", "navigate", "screenshot", "read_page", "get_page_text", "click", "type", "key", "scroll"} {
		want["browser_"+n] = true
	}
	for _, tl := range mcpToolList() {
		n, _ := tl["name"].(string)
		if !strings.HasPrefix(n, "browser_") {
			continue
		}
		if !want[n] {
			t.Errorf("unexpected %s", n)
		}
		delete(want, n)
		if d, _ := tl["description"].(string); !strings.Contains(d, "asks the person") {
			t.Errorf("%s does not say it asks the person", n)
		}
	}
	for n := range want {
		t.Errorf("%s is not listed", n)
	}
}

// A screenshot answer is an image block; text answers are text; a refusal
// is an error the agent reads.
func TestBrowserAnswersAsBlocks(t *testing.T) {
	enc := func(v string) string { return base64.StdEncoding.EncodeToString([]byte(v)) }
	b, err := browserBlocks(enc(`{"ok":true,"result":{"image":"AAA","mimeType":"image/png","note":"800x600"}}`))
	if err != nil || len(b) != 2 || b[0].(map[string]any)["type"] != "image" || b[0].(map[string]any)["data"] != "AAA" {
		t.Fatalf("%v, %v", b, err)
	}
	b, err = browserBlocks(enc(`{"ok":true,"result":"Clicked."}`))
	if err != nil || b[0].(map[string]any)["text"] != "Clicked." {
		t.Fatalf("%v, %v", b, err)
	}
	if _, err := browserBlocks(enc(`{"ok":false,"error":"The person did not allow browser use for this session."}`)); err == nil || !strings.Contains(err.Error(), "did not allow") {
		t.Fatalf("%v", err)
	}
}
