// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A window at (100,50) 400x300 holding a panel with two buttons and a text
// field; the snapshot is agent-desktop's shape (screen points).
const axTestSnapshot = `{"version":"2.6","ok":true,"command":"snapshot","data":{"tree":{"role":"window","name":"Calc","children":[
 {"role":"group","ref_id":"@s1:e1","bounds":{"x":100,"y":50,"width":300,"height":250},"children":[
  {"role":"button","name":"7","ref_id":"@s1:e2","bounds":{"x":110,"y":60,"width":40,"height":40}},
  {"role":"button","name":"8","ref_id":"@s1:e3","bounds":{"x":160,"y":60,"width":40,"height":40},"states":["disabled"]},
  {"role":"textfield","name":"Search","value":"hi","ref_id":"@s1:e4","bounds":{"x":110,"y":200,"width":200,"height":30}},
  {"role":"securetextfield","name":"Password","value":"hunter2","ref_id":"@s1:e5","bounds":{"x":110,"y":240,"width":200,"height":30}},
  {"role":"button","name":"Offscreen","ref_id":"@s1:e6","bounds":{"x":900,"y":900,"width":40,"height":40}}
 ]}]}}}`

var axTestWin = winInfo{ID: "w1", App: "Calc", X: 100, Y: 50, W: 400, H: 300}

func TestAXFlattenPlacesElementsInWindowFractions(t *testing.T) {
	var data struct {
		Data struct {
			Tree axNode `json:"tree"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(axTestSnapshot), &data); err != nil {
		t.Fatal(err)
	}
	els := axFlatten(data.Data.Tree, axTestWin, nil)
	byName := map[string]axEl{}
	for _, e := range els {
		byName[e.Name] = e
	}
	if _, ok := byName["Offscreen"]; ok {
		t.Error("an element outside the window was kept")
	}
	seven := byName["7"]
	if seven.X != 10.0/400 || seven.Y != 10.0/300 || seven.W != 40.0/400 || seven.Tap != "click" || !seven.Enabled {
		t.Errorf("7 = %+v", seven)
	}
	if byName["8"].Enabled {
		t.Error("a disabled button came back enabled")
	}
	if f := byName["Search"]; f.Tap != "edit" || f.Value != "hi" {
		t.Errorf("text field = %+v, want an edit tap with its value", f)
	}
	if p := byName["Password"]; !p.Secret || p.Value != "" {
		t.Errorf("password field = %+v: its value must never leave the host", p)
	}
}

func TestAXHitPicksTheSmallestEnabledControl(t *testing.T) {
	els := []axEl{
		{Ref: "panel", X: 0, Y: 0, W: 1, H: 1, Enabled: true},
		{Ref: "seven", X: 0.1, Y: 0.1, W: 0.1, H: 0.1, Enabled: true},
		{Ref: "off", X: 0.3, Y: 0.1, W: 0.1, H: 0.1, Enabled: false},
	}
	if h := axHit(els, 0.15, 0.15); h == nil || h.Ref != "seven" {
		t.Errorf("hit = %+v, want seven", h)
	}
	if h := axHit(els, 0.35, 0.15); h == nil || h.Ref != "panel" {
		t.Errorf("a disabled control took the tap: %+v", h)
	}
	if h := axHit(els[1:], 0.9, 0.9); h != nil {
		t.Errorf("hit outside every control = %+v, want none (pixel fallback)", h)
	}
}

func TestAXActRefusesAnythingButAWhitelistedVerbOnARef(t *testing.T) {
	var d axDesktop
	for _, c := range []struct{ ref, action string }{
		{"@s1:e2", "launch"}, {"@s1:e2", "close-app"}, {"--help", "click"}, {"@s1:e2 extra", "click"}, {"", "click"},
	} {
		if err := d.act(axTestWin, c.ref, c.action, ""); err == nil {
			t.Errorf("act(%q, %q) ran", c.ref, c.action)
		}
	}
}

// fakeAgentDesktop installs a stand-in agent-desktop that logs its arguments
// and answers a snapshot with axTestSnapshot and anything else with ok.
func fakeAgentDesktop(t *testing.T) (log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in is a shell script")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	snap := filepath.Join(dir, "snap.json")
	if err := os.WriteFile(snap, []byte(axTestSnapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\n" +
		"case \"$1\" in snapshot) cat " + snap + ";; *) echo '{\"ok\":true,\"data\":{}}';; esac\n"
	bin := filepath.Join(dir, "agent-desktop")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMINAL_AGENT_DESKTOP", bin)
	return log
}

func TestAXServeTapPressesTheControlUnderThePoint(t *testing.T) {
	log := fakeAgentDesktop(t)
	var d axDesktop
	b := &countingBackend{wins: []winInfo{axTestWin}}

	// A tap on "7" (window point 30,30) clicks it, by ref, without pixels.
	rep := d.serve(b, axReq{ID: "w1", Op: "tap", X: 30.0 / 400, Y: 30.0 / 300})
	if !rep.OK || rep.Pixel || rep.Did != "click" || rep.Hit == nil || rep.Hit.Name != "7" {
		t.Fatalf("tap = %+v", rep)
	}
	// A text field is reported for editing, not clicked.
	rep = d.serve(b, axReq{ID: "w1", Op: "tap", X: 50.0 / 400, Y: 165.0 / 300})
	if !rep.OK || rep.Did != "edit" || rep.Hit.Name != "Search" {
		t.Fatalf("tap on a field = %+v", rep)
	}
	// Outside every control: the pixel fallback.
	rep = d.serve(b, axReq{ID: "w1", Op: "tap", X: 0.999, Y: 0.999})
	if !rep.OK || !rep.Pixel {
		t.Fatalf("tap on nothing = %+v, want the pixel fallback", rep)
	}
	// set-value passes the text after "--", so text that starts with "-" is text.
	rep = d.serve(b, axReq{ID: "w1", Op: "act", Ref: "@s1:e4", Action: "set-value", Value: "-rf /"})
	if !rep.OK {
		t.Fatalf("set-value = %+v", rep)
	}
	raw, _ := os.ReadFile(log)
	calls := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{"click @s1:e2", "set-value @s1:e4 -- -rf /"}
	for _, w := range want {
		found := false
		for _, c := range calls {
			if c == w {
				found = true
			}
		}
		if !found {
			t.Errorf("agent-desktop was not run as %q; calls:\n%s", w, raw)
		}
	}
}

func TestAXServeFindLocatesAnElementByRoleAndName(t *testing.T) {
	fakeAgentDesktop(t)
	var d axDesktop
	b := &countingBackend{wins: []winInfo{axTestWin}}
	rep := d.serve(b, axReq{ID: "w1", Op: "find", Role: "textfield", Name: "Search"})
	if !rep.OK || rep.Hit == nil || rep.Hit.Ref != "@s1:e4" {
		t.Fatalf("find = %+v", rep)
	}
	rep = d.serve(b, axReq{ID: "w1", Op: "find", Role: "button", Name: "Nope"})
	if rep.OK || rep.Hit != nil {
		t.Fatalf("find of a missing element = %+v", rep)
	}
}

func TestAXTraceTailReadsOnlyNewWholeLines(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "run-1", "trace")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "1-old.jsonl")
	if err := os.WriteFile(old, []byte(`{"event":"action.dispatch.start","ref":"@a:e1"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tail := newAXTraceTail(root)
	if evs := tail.poll(); len(evs) != 0 {
		t.Fatalf("history from before the watch began came back: %+v", evs)
	}
	cur := filepath.Join(dir, "2-new.jsonl")
	f, err := os.Create(cur)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.WriteString(`{"event":"action.dispatch.start","action":"click","ref":"@s9:e3","session_id":"run-1"}` + "\n" + `{"event":"comm`)
	evs := tail.poll()
	if len(evs) != 1 || evs[0].Ref != "@s9:e3" || evs[0].Action != "click" {
		t.Fatalf("first poll = %+v, want the one whole line", evs)
	}
	_, _ = f.WriteString(`and.end","ok":true}` + "\n")
	if evs := tail.poll(); len(evs) != 1 || evs[0].Event != "command.end" {
		t.Fatalf("second poll = %+v, want the line finished since", evs)
	}
	if evs := tail.poll(); len(evs) != 0 {
		t.Fatalf("a line came back twice: %+v", evs)
	}
}

func TestAXLookupRefReadsTheSnapshotRefmap(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "run-1", "snapshots", "s9")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	refmap := `{"inner":{"@e3":{"pid":42,"role":"button","name":"8","source_app":"Calculator","source_window_id":"w-23"}},"counter":3}`
	if err := os.WriteFile(filepath.Join(dir, "refmap.json"), []byte(refmap), 0o600); err != nil {
		t.Fatal(err)
	}
	info, ok := axLookupRef(root, "run-1", "@s9:e3")
	if !ok || info.Role != "button" || info.Name != "8" || info.WindowID != "w-23" || info.PID != 42 {
		t.Fatalf("lookup = %+v, %v", info, ok)
	}
	if _, ok := axLookupRef(root, "run-1", "@s9:e4"); ok {
		t.Error("a ref missing from the refmap resolved")
	}
	if _, ok := axLookupRef(root, "run-1", "garbage"); ok {
		t.Error("a malformed ref resolved")
	}
}

// A window whose tree shows only its frame until --force-electron-a11y.
const axTestSparse = `{"ok":true,"data":{"tree":{"role":"window","children":[
 {"role":"button","name":"Close","ref_id":"@s2:e1","bounds":{"x":105,"y":55,"width":12,"height":12}}]}}}`

// fakeElectronDesktop answers a snapshot with axTestSnapshot only when asked
// with --force-electron-a11y (and when rich is set), else with axTestSparse.
func fakeElectronDesktop(t *testing.T, rich bool) (log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in is a shell script")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	full, sparse := filepath.Join(dir, "full.json"), filepath.Join(dir, "sparse.json")
	if !rich {
		full = sparse
	}
	_ = os.WriteFile(filepath.Join(dir, "full.json"), []byte(axTestSnapshot), 0o600)
	_ = os.WriteFile(sparse, []byte(axTestSparse), 0o600)
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\n" +
		"case \"$*\" in *--force-electron-a11y*) cat " + full + ";; *) cat " + sparse + ";; esac\n"
	bin := filepath.Join(dir, "agent-desktop")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMINAL_AGENT_DESKTOP", bin)
	old := axForceSupported
	axForceSupported = func() bool { return true }
	t.Cleanup(func() { axForceSupported = old })
	return log
}

func axCalls(t *testing.T, log string) []string {
	raw, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestAXSparseTreeRetriesWithElectronAccessibilityOnce(t *testing.T) {
	log := fakeElectronDesktop(t, true)
	var d axDesktop
	els, err := d.snapshot(axTestWin, true)
	if err != nil || len(els) < 5 {
		t.Fatalf("snapshot = %d controls, %v; want the full tree (5) after the retry", len(els), err)
	}
	// The app is now known to need it: the next read asks once, with the flag.
	if _, err := d.snapshot(axTestWin, true); err != nil {
		t.Fatal(err)
	}
	calls := axCalls(t, log)
	if len(calls) != 3 || strings.Contains(calls[0], "--force-electron-a11y") ||
		!strings.Contains(calls[1], "--force-electron-a11y") || !strings.Contains(calls[2], "--force-electron-a11y") {
		t.Fatalf("calls = %q; want plain, forced, then forced only", calls)
	}
}

func TestAXGenuinelySparseAppIsNotRetriedEveryTime(t *testing.T) {
	log := fakeElectronDesktop(t, false)
	var d axDesktop
	for i := 0; i < 3; i++ {
		if _, err := d.snapshot(axTestWin, true); err != nil {
			t.Fatal(err)
		}
	}
	// One plain read, one forced retry that gained nothing, then plain reads.
	if calls := axCalls(t, log); len(calls) != 4 {
		t.Fatalf("calls = %q; want 4 (the retry happens once per app)", calls)
	}
}

func TestAXElectronBundleIsForcedFromTheFirstRead(t *testing.T) {
	log := fakeElectronDesktop(t, true)
	app := filepath.Join(t.TempDir(), "Slack.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "Frameworks", "Electron Framework.framework"), 0o700); err != nil {
		t.Fatal(err)
	}
	w := axTestWin
	w.AppPath = app
	var d axDesktop
	if _, err := d.snapshot(w, true); err != nil {
		t.Fatal(err)
	}
	if calls := axCalls(t, log); len(calls) != 1 || !strings.Contains(calls[0], "--force-electron-a11y") {
		t.Fatalf("calls = %q; want one forced read", calls)
	}
}
