// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package cloudflare

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// voiceUIScript runs the viewer's real voiceui.js (VOICEUI) in node's vm,
// against a small stand-in for the page: the mic button, the body, the frame
// it makes, and the app.js names voiceui.js reads.
const voiceUIScript = `
import vm from "node:vm";
import fs from "node:fs";
function classes() {
  const s = new Set();
  return { add: (...c) => c.forEach((x) => s.add(x)), remove: (...c) => c.forEach((x) => s.delete(x)),
    toggle(c, on) { if (on === undefined) on = !s.has(c); on ? s.add(c) : s.delete(c); return on; }, contains: (c) => s.has(c) };
}
function el(tag) {
  const e = { tagName: tag, children: [], style: {}, hidden: false, title: "", textContent: "", className: "", id: "", listeners: {},
    classList: classes(), setAttribute() {}, setPointerCapture() {},
    appendChild(c) { e.children.push(c); return c; }, append(...c) { c.forEach((x) => e.appendChild(x)); },
    addEventListener(t, f) { (e.listeners[t] ||= []).push(f); },
    getBoundingClientRect: () => ({ left: 0, top: 0, right: 10, bottom: 10, width: 10, height: 10 }) };
  Object.defineProperty(e, "innerHTML", { set() { e.children = []; }, get: () => "" });
  if (tag === "iframe") e.contentWindow = { posted: [], postMessage(m) { this.posted.push(m); } };
  return e;
}
const btn = el("button"), desk = el("button"), body = el("body"), sent = [], winListeners = {};
const ctx = {
  document: { getElementById: (id) => ({ "voice-btn": btn, "voice-btn-desk": desk })[id] || null, createElement: el, body, addEventListener() {}, removeEventListener() {} },
  location: { origin: "https://viewer.test" }, innerWidth: 800, innerHeight: 600, console,
  setTimeout: () => 0, clearTimeout() {},
  addEventListener(t, f) { (winListeners[t] ||= []).push(f); },
  sessionId: "S1", lastHostInfo: null, loadHistory: () => [], sendWin: (...a) => sent.push(a),
};
ctx.window = ctx;
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(process.env.VOICEUI, "utf8"), ctx);
const V = ctx.reminalVoice;
const byId = (id) => body.children.find((c) => c.id === id);
const dockWords = () => { const d = byId("voicedock"); return d && d.classList.contains("open") ? d.children.map((r) => r.children[0].textContent).join(" | ") : ""; };
const tap = () => btn.listeners.click.forEach((f) => f({ timeStamp: 100000 }));
const fromFrame = (data) => { const f = byId("voice-panel").children[0]; winListeners.message.forEach((h) => h({ source: f.contentWindow, origin: "https://viewer.test", data: { src: "reminal-voice", ...data } })); };
const posted = () => { const p = byId("voice-panel"); return p ? p.children[0].contentWindow.posted.map((m) => m.t + (m.c ? ":" + m.c : "")) : []; };
const look = (name) => console.log(JSON.stringify({ name, novoice: btn.classList.contains("novoice"), desk: ["novoice", "live", "hear"].filter((c) => desk.classList.contains(c)).join(" "), live: btn.classList.contains("live"), title: btn.title, panel: byId("voice-panel") ? (byId("voice-panel").hidden ? "hidden" : "shown") : "none", dock: dockWords(), posted: posted() }));

// A session whose reminal predates voice: host_info without "voice".
V.hostInfo({ attn: "" });
look("old-host");
tap();
look("old-host-tap");
// A newer one: the tap opens the conversation.
V.hostInfo({ attn: "", voice: true });
look("new-host");
tap();
look("new-host-tap");
fromFrame({ t: "ready" });
fromFrame({ t: "state", running: true, cls: "hear", text: "Listening" });
look("running");
// Switching, with voice on, to a session whose reminal predates voice.
ctx.sessionId = "S2";
V.sessionChanged();
V.hostInfo({ attn: "" });
look("switched-to-old");
`

func TestVoiceNeedsANewerReminalWhereTheSessionHasNone(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	js, err := filepath.Abs(filepath.Join("..", "internal", "client", "web", "voiceui.js"))
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(t.TempDir(), "main.mjs")
	if err := os.WriteFile(main, []byte(voiceUIScript), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, main)
	cmd.Env = append(os.Environ(), "VOICEUI="+js)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	type look struct {
		Name    string   `json:"name"`
		NoVoice bool     `json:"novoice"`
		Desk    string   `json:"desk"`
		Live    bool     `json:"live"`
		Title   string   `json:"title"`
		Panel   string   `json:"panel"`
		Dock    string   `json:"dock"`
		Posted  []string `json:"posted"`
	}
	got := map[string]look{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var l look
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatalf("%v: %q", err, line)
		}
		got[l.Name] = l
	}
	const need = "Voice needs a newer reminal on that machine"
	// The header's mic (a desktop viewer's) shows what the key bar's does.
	if l := got["old-host"]; l.Desk != "novoice" {
		t.Errorf("the header mic on an old host: %q", l.Desk)
	}
	if l := got["running"]; !l.Live || l.Desk != "live hear" {
		t.Errorf("voice on: key bar mic live %v, header mic %q", l.Live, l.Desk)
	}
	if l := got["old-host"]; !l.NoVoice || l.Title != need || l.Panel != "none" {
		t.Errorf("an old host: %+v", l)
	}
	if l := got["old-host-tap"]; l.Panel != "none" || l.Dock != need {
		t.Errorf("a tap on an old host opened voice or said nothing: %+v", l)
	}
	if l := got["new-host"]; l.NoVoice || l.Title == need {
		t.Errorf("a new host: %+v", l)
	}
	if l := got["new-host-tap"]; l.Panel != "shown" {
		t.Errorf("a tap on a new host: %+v", l)
	}
	l := got["switched-to-old"]
	if l.Panel != "hidden" || l.Dock != need || !l.NoVoice {
		t.Errorf("switched to an old host with voice on: %+v", l)
	}
	if !strings.Contains(strings.Join(l.Posted, " "), "cmd:stop") {
		t.Errorf("voice was not stopped on the switch: %v", l.Posted)
	}
	if strings.ContainsAny(need, "…—") {
		t.Errorf("%q has an ellipsis or an em dash", need)
	}
}
