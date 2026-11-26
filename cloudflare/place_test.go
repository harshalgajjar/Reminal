// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package cloudflare

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Where a session is served is told to its agent as the codes Cloudflare
// names its locations by, read from Cloudflare's own trace page and from the
// request. Both are outside input: only three capital letters are a code,
// and anything else is told as nothing. The functions are TypeScript, run
// here as they ship (their type annotations removed).
func TestRelayPlaceCodes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	src, err := os.ReadFile(filepath.Join("src", "place.ts"))
	if err != nil {
		t.Fatal(err)
	}
	fn := string(src)
	for from, to := range map[string]string{
		"export function placeCode(v: unknown): string {":        "function placeCode(v) {",
		"export function placeFromTrace(text: string): string {": "function placeFromTrace(text) {",
	} {
		if !strings.Contains(fn, from) {
			t.Fatalf("place.ts changed (%q missing): update this test", from)
		}
		fn = strings.Replace(fn, from, to, 1)
	}
	script := fn + `
const cases = [
  [placeCode("SJC"), "SJC"], [placeCode("sjc"), ""], [placeCode("SJCX"), ""], [placeCode("S1C"), ""],
  [placeCode(""), ""], [placeCode(undefined), ""], [placeCode(42), ""], [placeCode("SJ\n"), ""],
  [placeFromTrace("fl=1f1\nh=www.cloudflare.com\nip=203.0.113.9\ncolo=BOM\nloc=IN\n"), "BOM"],
  [placeFromTrace("colo=bom\n"), ""], [placeFromTrace("xcolo=BOM\n"), ""], [placeFromTrace("colo=BOM<script>\n"), ""],
  [placeFromTrace("colo= SIN \n"), "SIN"], [placeFromTrace(""), ""], [placeFromTrace("loc=IN\n"), ""],
];
const bad = cases.map(([got, want], i) => got === want ? "" : "case " + i + ": got " + JSON.stringify(got) + ", want " + JSON.stringify(want)).filter(Boolean);
if (bad.length) { console.log(bad.join("\n")); process.exit(1); }
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("place.ts: %v\n%s", err, out)
	}
}

// Only an agent is told: a viewer's socket is answered before its PIN is
// checked, and where a machine is served from is not for whoever knows a
// session's id. The header the place travels in is the relay's own, never
// the caller's.
func TestOnlyTheAgentIsToldWhereItsSessionIsServed(t *testing.T) {
	session, err := os.ReadFile(filepath.Join("src", "session.ts"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(session)
	if n := strings.Count(s, "this.relayPlace("); n != 1 {
		t.Fatalf("relayPlace is called in %d places, want the agent's auth_ok alone", n)
	}
	if !strings.Contains(s, `attachment.role === "agent" ? await this.relayPlace(attachment.edge) : null`) {
		t.Fatal("relayPlace is no longer told to the agent's role alone")
	}
	if !strings.Contains(s, `const edge = placeCode(request.headers.get("x-reminal-edge"));`) {
		t.Fatal("the socket's edge is no longer read through placeCode")
	}
	// A viewer's answer carries its own edge and nothing else.
	if !strings.Contains(s, `attachment.edge ? { type: "auth_ok", relay: { edge: attachment.edge } } : { type: "auth_ok" }`) {
		t.Fatal("a viewer's auth_ok is no longer its own edge alone")
	}
	if n := strings.Count(s, "relay: {"); n != 1 {
		t.Fatalf("%d answers are built with a relay of their own, want the viewer's edge alone", n)
	}
	index, err := os.ReadFile(filepath.Join("src", "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	i := string(index)
	del, set := strings.Index(i, `hdrs.delete("x-reminal-edge");`), strings.Index(i, `hdrs.set("x-reminal-edge", colo)`)
	if del < 0 || set < 0 || set < del {
		t.Fatal("the caller's own x-reminal-edge is no longer removed before the relay sets its own")
	}
}
