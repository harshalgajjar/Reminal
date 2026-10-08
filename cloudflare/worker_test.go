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

// A forwarded app is somebody's arbitrary HTML. Served under /p/<id>/ on the
// relay's own host it would share an origin with the viewer — the origin whose
// localStorage and IndexedDB hold this browser's remembered sessions and its
// owner key — so it is sent to an origin of its own instead. tunnelOrigin is
// what decides that, and it decides it from a Host header, which is attacker-
// controlled input.
//
// The function is TypeScript, so this runs the real source through node rather
// than restating the rules in Go, where they could drift apart.
func TestTunnelOriginRouting(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	src, err := os.ReadFile(filepath.Join("src", "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(src), "export function tunnelOrigin")
	end := strings.Index(string(src), "export default {")
	if start < 0 || end < 0 || end < start {
		t.Fatal("tunnelOrigin is no longer where this test looks for it — fix the test, do not delete it")
	}
	fn := string(src)[start:end]
	// The handful of type annotations the function carries, removed so plain
	// node runs the same body that ships.
	for from, to := range map[string]string{
		"hostHeader: string,":            "hostHeader,",
		"suffixVar: string | undefined,": "suffixVar,",
		"sessionId: string,":             "sessionId,",
		"): string {":                    ") {",
	} {
		if !strings.Contains(fn, from) {
			t.Fatalf("tunnelOrigin's signature changed (%q missing) — update this test", from)
		}
		fn = strings.Replace(fn, from, to, 1)
	}

	cases := []struct{ host, suffix, want string }{
		// The case this exists for: the relay's own host must never serve one.
		{"live.reminal.app", "reminal.app", "https://port-abc12345.reminal.app"},
		{"live.reminal.app:443", "reminal.app", "https://port-abc12345.reminal.app"},
		{"LIVE.REMINAL.APP", "reminal.app", "https://port-abc12345.reminal.app"},
		{"reminal.app", "reminal.app", "https://port-abc12345.reminal.app"},
		// Already on the forward's own origin: serve it, and do not loop.
		{"port-abc12345.reminal.app", "reminal.app", ""},
		{"port-abc12345.reminal.app:443", "reminal.app", ""},
		// A deployment without wildcard subdomains keeps serving at the path:
		// staging on workers.dev, `wrangler dev`, a self-hosted relay.
		{"reminal-relay-staging.futuristic.workers.dev", "", ""},
		{"localhost", "", ""},
		{"127.0.0.1:8787", "", ""},
		// A suffix is set, but this host is not under it. A lookalike domain
		// must not be treated as ours.
		{"example.com", "reminal.app", ""},
		{"reminal.app.evil.com", "reminal.app", ""},
		{"notreminal.app", "reminal.app", ""},
		{"", "reminal.app", ""},
	}

	var b strings.Builder
	b.WriteString(fn)
	b.WriteString("\nconst cases = [\n")
	for _, c := range cases {
		b.WriteString("  [" + jsStr(c.host) + "," + jsStr(c.suffix) + "," + jsStr(c.want) + "],\n")
	}
	b.WriteString(`];
let bad = [];
for (const [host, suffix, want] of cases) {
  const got = tunnelOrigin(host, suffix, "ABC12345");
  if (got !== want) bad.push(JSON.stringify({host, suffix, got, want}));
}
console.log(bad.length ? bad.join("\n") : "PASS");
`)
	dir := t.TempDir()
	script := filepath.Join(dir, "t.mjs")
	if err := os.WriteFile(script, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("running tunnelOrigin: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "PASS" {
		t.Fatalf("tunnelOrigin routed these wrongly:\n%s", out)
	}
}

func jsStr(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// addressGroup in session.ts must group an IPv6 address by its /64 and leave
// an IPv4 address as it is.
func TestAddressGroupByIPv6Prefix(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	src, err := os.ReadFile(filepath.Join("src", "session.ts"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	start := strings.Index(s, "export function addressGroup(ip: string): string {")
	if start < 0 {
		t.Fatal("addressGroup is no longer where this test looks for it — fix the test, do not delete it")
	}
	end := strings.Index(s[start:], "\n}\n")
	fn := strings.Replace(s[start:start+end+2], "export function addressGroup(ip: string): string {", "function addressGroup(ip) {", 1)
	cases := map[string]string{
		"203.0.113.7":                "203.0.113.7",
		"2001:db8:1:2:aaaa::1":       "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::9999":    "2001:db8:1:2::/64",
		"2001:db8::1":                "2001:db8:0:0::/64",
		"::1":                        "0:0:0:0::/64",
		"::ffff:203.0.113.7":         "203.0.113.7",
		"0:0:0:0:0:ffff:203.0.113.7": "203.0.113.7",
		"::ffff:cb00:7107":           "203.0.113.7",
	}
	for in, want := range cases {
		out, err := exec.Command(node, "-e", fn+"\nprocess.stdout.write(addressGroup("+jsString(in)+"))").CombinedOutput()
		if err != nil {
			t.Fatalf("node: %v\n%s", err, out)
		}
		if string(out) != want {
			t.Errorf("addressGroup(%q) = %q, want %q", in, out, want)
		}
	}
}

func jsString(s string) string { return "'" + strings.ReplaceAll(s, "'", "\\'") + "'" }

// Every alarm a room sets goes through armAlarm (src/alarm.ts), which bounds
// how often a room can run on its own: an alarm that re-arms itself without
// a bound bills for every run, and Cloudflare has no spend cap. A direct
// storage.setAlarm anywhere else would sidestep it.
func TestEveryAlarmGoesThroughArmAlarm(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("src", "*.ts"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no Worker sources found: %v", err)
	}
	for _, f := range files {
		if filepath.Base(f) == "alarm.ts" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, ".setAlarm(") {
				t.Errorf("%s:%d sets an alarm directly; use armAlarm: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
