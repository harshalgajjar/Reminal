// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// A window must be raised when it is not already the front one, and NOT
// re-raised while it stays there. Keying this on gesture timing could not
// work: the raise costs ~410ms (focus walks every window of the app over the
// Accessibility bridge), so scroll events queued further apart than the 400ms
// "same gesture" window they were tested against, and every one paid the raise
// again. The cost being avoided defeated the test for avoiding it.
func TestMirrorRaisesOnlyWhenTheFrontWindowChanges(t *testing.T) {
	reset := func() {
		mirrorInput.front = frontWindowTracker{}
	}

	t.Run("first event on a window raises it", func(t *testing.T) {
		reset()
		if !mirrorInput.front.needsRaise("w1") {
			t.Fatal("a window not known to be in front must be raised")
		}
	})

	t.Run("staying on it does not, however slowly events arrive", func(t *testing.T) {
		reset()
		mirrorInput.front.needsRaise("w1")
		for i := 0; i < 20; i++ {
			// Pretend each event took longer than the old gesture gap. That is
			// the case the previous rule got wrong.
			mirrorInput.front.mu.Lock()
			mirrorInput.front.at = time.Now().Add(-winLookupReuseTTL - 200*time.Millisecond)
			mirrorInput.front.mu.Unlock()
			if mirrorInput.front.needsRaise("w1") {
				t.Fatalf("event %d re-raised a window already in front", i+2)
			}
		}
	})

	t.Run("a different window raises immediately", func(t *testing.T) {
		reset()
		mirrorInput.front.needsRaise("w1")
		if !mirrorInput.front.needsRaise("w2") {
			t.Fatal("switching windows must raise the new one")
		}
		if mirrorInput.front.needsRaise("w2") {
			t.Fatal("re-raised the window it just raised")
		}
	})

	t.Run("a click elsewhere makes the next scroll re-raise", func(t *testing.T) {
		reset()
		mirrorInput.front.needsRaise("w1")
		mirrorInput.front.note("w2") // a click raised something else
		if !mirrorInput.front.needsRaise("w1") {
			t.Fatal("scrolling w1 after w2 was raised must raise w1 again")
		}
	})

	t.Run("the record goes stale so an external focus change is picked up", func(t *testing.T) {
		reset()
		mirrorInput.front.needsRaise("w1")
		mirrorInput.front.mu.Lock()
		mirrorInput.front.at = time.Now().Add(-frontWindowTTL - time.Millisecond)
		mirrorInput.front.mu.Unlock()
		if !mirrorInput.front.needsRaise("w1") {
			t.Fatal("a stale record must be re-established")
		}
	})
}

// countingBackend records how many times the window list was enumerated. On
// macOS that enumeration is an osascript over every window on the system,
// measured at ~114ms against the live daemon — the single most expensive thing
// an input event did, and it did it every time.
type countingBackend struct {
	windowBackend
	lists int
	wins  []winInfo
}

func (c *countingBackend) list() ([]winInfo, error) { c.lists++; return c.wins, nil }

func TestMirrorFindWindowReusesLookupWithinAGesture(t *testing.T) {
	reset := func() {
		winLookupMu.Lock()
		winLookupCache = map[string]winLookupEntry{}
		winLookupMu.Unlock()
	}
	b := func() *countingBackend {
		return &countingBackend{wins: []winInfo{{ID: "w1", W: 800, H: 600}}}
	}

	t.Run("a scroll gesture enumerates once, not per event", func(t *testing.T) {
		reset()
		c := b()
		for i := 0; i < 20; i++ { // a second of scrolling at the viewer's cadence
			if _, err := resolveWindowFor(c, "w1", true); err != nil {
				t.Fatalf("event %d: %v", i, err)
			}
		}
		if c.lists != 1 {
			t.Fatalf("enumerated %d times for one gesture, want 1", c.lists)
		}
	})

	t.Run("clicks always re-resolve", func(t *testing.T) {
		reset()
		c := b()
		for i := 0; i < 3; i++ {
			// reuse=false is what a click/drag passes: placing a pointer on a
			// stale rectangle is a click in the wrong place.
			if _, err := resolveWindowFor(c, "w1", false); err != nil {
				t.Fatal(err)
			}
		}
		if c.lists != 3 {
			t.Fatalf("enumerated %d times, want a fresh lookup per click (3)", c.lists)
		}
	})

	t.Run("the entry expires so a moved window is picked up", func(t *testing.T) {
		reset()
		c := b()
		resolveWindowFor(c, "w1", true)
		winLookupMu.Lock()
		e := winLookupCache["w1"]
		e.at = time.Now().Add(-winLookupReuseTTL - time.Millisecond)
		winLookupCache["w1"] = e
		winLookupMu.Unlock()
		resolveWindowFor(c, "w1", true)
		if c.lists != 2 {
			t.Fatalf("enumerated %d times, want a re-resolve after expiry (2)", c.lists)
		}
	})

	t.Run("a closed window is not served from cache", func(t *testing.T) {
		reset()
		c := b()
		resolveWindowFor(c, "w1", true)
		c.wins = nil // window closed
		winLookupMu.Lock()
		e := winLookupCache["w1"]
		e.at = time.Now().Add(-winLookupReuseTTL - time.Millisecond)
		winLookupCache["w1"] = e
		winLookupMu.Unlock()
		if _, err := resolveWindowFor(c, "w1", true); err == nil {
			t.Fatal("resolved a window that is gone")
		}
		winLookupMu.Lock()
		_, still := winLookupCache["w1"]
		winLookupMu.Unlock()
		if still {
			t.Fatal("stale entry survived a failed lookup")
		}
	})
}

// Which events may reuse a resolved window is a correctness question, not just
// a performance one: anything that places a pointer at an exact spot must see
// the window's current rectangle, or the pointer lands somewhere else.
func TestMirrorReuseLookupOnlyWhereGeometryIsStable(t *testing.T) {
	for _, tc := range []struct {
		kind, phase string
		want        bool
		why         string
	}{
		{"scroll", "", true, "repeats many times a second under a fixed pointer"},
		{"key", "", true, "does not use geometry at all"},
		{"drag", "begin", false, "the press must land on the current rectangle"},
		{"drag", "move", true, "the window cannot move out from under a live drag"},
		{"drag", "end", true, "continues the same gesture"},
		{"drag", "", false, "legacy whole-path replay places its own press"},
		{"click", "", false, "a stale rectangle is a click in the wrong place"},
	} {
		if got := reuseLookupFor(tc.kind, tc.phase); got != tc.want {
			t.Errorf("reuseLookupFor(%q, %q) = %v, want %v — %s",
				tc.kind, tc.phase, got, tc.want, tc.why)
		}
	}
}

// The phased protocol is opt-in per host. A viewer that sent phases to a host
// which replays whole paths would press and release once per chunk — a burst
// of clicks where the user meant one drag — so the flag has to track which
// backends actually implement the phases.
func TestDragPhasesAdvertisedOnlyWhereImplemented(t *testing.T) {
	h := gatherHostInfo()
	if want := runtime.GOOS == "darwin"; h.DragPhases != want {
		t.Fatalf("DragPhases = %v on %s, want %v", h.DragPhases, runtime.GOOS, want)
	}
}

// recordingBackend records what a backend was actually asked to do.
type recordingBackend struct {
	windowBackend
	wins  []winInfo
	calls []string
}

func (r *recordingBackend) list() ([]winInfo, error) { return r.wins, nil }
func (r *recordingBackend) focus(w winInfo) error {
	r.calls = append(r.calls, "focus")
	return nil
}
func (r *recordingBackend) clickN(w winInfo, fx, fy float64, n int, right bool) error {
	r.calls = append(r.calls, fmt.Sprintf("click(n=%d,right=%v)", n, right))
	return nil
}
func (r *recordingBackend) scroll(w winInfo, fx, fy, dx, dy float64) error {
	r.calls = append(r.calls, "scroll")
	return nil
}
func (r *recordingBackend) drag(w winInfo, pts [][2]float64) error {
	r.calls = append(r.calls, fmt.Sprintf("drag(path=%d)", len(pts)))
	return nil
}

func newRecording() *recordingBackend {
	return &recordingBackend{wins: []winInfo{{ID: "w1", W: 800, H: 600}}}
}

// One dispatcher serves both injection paths. It previously existed twice and
// the copies drifted every time something was added: the daemon — the path
// macOS actually takes — never gained the click-count fallback, so an older
// viewer that reports no count got a single click where it meant a double.
func TestApplyWindowInputIsOneImplementation(t *testing.T) {
	fresh := func() (*recordingBackend, *inputState) {
		winLookupMu.Lock()
		winLookupCache = map[string]winLookupEntry{}
		winLookupMu.Unlock()
		return newRecording(), &inputState{}
	}

	t.Run("a viewer that reports no count still gets a double-click", func(t *testing.T) {
		b, st := fresh()
		ev := windowInput{ID: "w1", Kind: "click", X: 0.5, Y: 0.5} // Count unset
		applyWindowInput(b, st, ev, nil)
		applyWindowInput(b, st, ev, nil)
		if got := strings.Join(b.calls, " "); !strings.Contains(got, "click(n=2") {
			t.Fatalf("calls = %q, want a second click counted as n=2", got)
		}
	})

	t.Run("a reported count is trusted over the fallback", func(t *testing.T) {
		b, st := fresh()
		applyWindowInput(b, st, windowInput{ID: "w1", Kind: "click", Count: 3}, nil)
		if got := strings.Join(b.calls, " "); !strings.Contains(got, "click(n=3") {
			t.Fatalf("calls = %q, want the viewer's own count", got)
		}
	})

	t.Run("a backend without phased drag drops phased events", func(t *testing.T) {
		b, st := fresh()
		// recordingBackend implements no dragPhase, standing in for the
		// platforms that never advertised drag_phases.
		applyWindowInput(b, st, windowInput{ID: "w1", Kind: "drag", Phase: "move",
			Path: [][2]float64{{0.1, 0.1}}}, nil)
		if len(b.calls) != 0 {
			t.Fatalf("calls = %v, want a phased event dropped, not replayed as a click burst", b.calls)
		}
	})

	t.Run("a whole-path drag still replays", func(t *testing.T) {
		b, st := fresh()
		applyWindowInput(b, st, windowInput{ID: "w1", Kind: "drag",
			Path: [][2]float64{{0.1, 0.1}, {0.2, 0.2}}}, nil)
		if got := strings.Join(b.calls, " "); !strings.Contains(got, "drag(path=2)") {
			t.Fatalf("calls = %q, want the batched drag", got)
		}
	})

	t.Run("scroll raises once and the click path records the raise", func(t *testing.T) {
		b, st := fresh()
		sc := windowInput{ID: "w1", Kind: "scroll"}
		applyWindowInput(b, st, sc, nil)
		applyWindowInput(b, st, sc, nil)
		if n := strings.Count(strings.Join(b.calls, " "), "focus"); n != 1 {
			t.Fatalf("focus called %d times for one window, want 1", n)
		}
	})

	t.Run("a right-click is reported to the menu hook", func(t *testing.T) {
		b, st := fresh()
		var gotRight, called = false, false
		applyWindowInput(b, st, windowInput{ID: "w1", Kind: "click", Button: "right", Count: 1},
			func(w winInfo, right bool) { called, gotRight = true, right })
		if !called || !gotRight {
			t.Fatalf("menu hook called=%v right=%v, want true/true", called, gotRight)
		}
	})
}

// A click used to pay the ~410ms macOS raise (focus walks every window of the
// app over the Accessibility bridge) on EVERY event — even when the window it
// targeted was already in front — so clicking around inside one window stuttered
// though the window never left the front. Clicks now gate on the same
// front-window tracker scroll uses: raise when the front window changes, never
// while it stays put. This is half of what makes a remote window feel local.
func TestClicksRaiseOnlyWhenTheFrontWindowChanges(t *testing.T) {
	fresh := func() (*recordingBackend, *inputState) {
		winLookupMu.Lock()
		winLookupCache = map[string]winLookupEntry{}
		winLookupMu.Unlock()
		return &recordingBackend{wins: []winInfo{{ID: "w1", W: 800, H: 600}, {ID: "w2", W: 800, H: 600}}}, &inputState{}
	}

	t.Run("repeated clicks on one window raise it once", func(t *testing.T) {
		b, st := fresh()
		click := windowInput{ID: "w1", Kind: "click", X: 0.5, Y: 0.5, Count: 1}
		for i := 0; i < 5; i++ {
			applyWindowInput(b, st, click, nil)
		}
		if n := strings.Count(strings.Join(b.calls, " "), "focus"); n != 1 {
			t.Fatalf("focus called %d times for five clicks on one window, want 1", n)
		}
		if n := strings.Count(strings.Join(b.calls, " "), "click("); n != 5 {
			t.Fatalf("click injected %d times, want all 5 to land", n)
		}
	})

	t.Run("a click after a scroll on the same window does not re-raise", func(t *testing.T) {
		b, st := fresh()
		applyWindowInput(b, st, windowInput{ID: "w1", Kind: "scroll"}, nil)
		applyWindowInput(b, st, windowInput{ID: "w1", Kind: "click", Count: 1}, nil)
		if n := strings.Count(strings.Join(b.calls, " "), "focus"); n != 1 {
			t.Fatalf("focus called %d times across a scroll then a click on the same window, want 1", n)
		}
	})

	t.Run("clicking a different window raises the new one", func(t *testing.T) {
		b, st := fresh()
		applyWindowInput(b, st, windowInput{ID: "w1", Kind: "click", Count: 1}, nil)
		applyWindowInput(b, st, windowInput{ID: "w2", Kind: "click", Count: 1}, nil)
		if n := strings.Count(strings.Join(b.calls, " "), "focus"); n != 2 {
			t.Fatalf("focus called %d times, want a raise for each of two different windows", n)
		}
	})
}

// draggingBackend records phased drag injection.
type draggingBackend struct {
	recordingBackend
	mu     sync.Mutex
	phases []string
}

func (d *draggingBackend) dragPhase(w winInfo, phase string, fx, fy float64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.phases = append(d.phases, phase)
	return nil
}
func (d *draggingBackend) got() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.phases...)
}

// A live drag presses on "begin" and releases on "end", so an end that never
// arrives leaves the host's desktop grabbed. The existing releases only fire
// when the last viewer leaves or a pane closes — neither happens when a socket
// drops mid-drag and the viewer RECONNECTS, so the button stayed down for as
// long as the session lived. The batched drag this replaced could not do that:
// it was one replay that always ended with a mouse-up.
func TestAbandonedDragReleasesTheButton(t *testing.T) {
	newDragging := func() *draggingBackend {
		winLookupMu.Lock()
		winLookupCache = map[string]winLookupEntry{}
		winLookupMu.Unlock()
		d := &draggingBackend{}
		d.wins = []winInfo{{ID: "w1", W: 800, H: 600}}
		return d
	}

	t.Run("a drag that stops arriving is released for it", func(t *testing.T) {
		b, st := newDragging(), &inputState{}
		applyWindowInput(b, st, windowInput{ID: "w1", Kind: "drag", Phase: "begin", X: 0.2, Y: 0.2}, nil)
		applyWindowInput(b, st, windowInput{ID: "w1", Kind: "drag", Phase: "move",
			Path: [][2]float64{{0.5, 0.5}}}, nil)
		// The viewer vanishes here — no "end" ever comes.
		st.drag.mu.Lock()
		armed := st.drag.timer != nil
		st.drag.mu.Unlock()
		if !armed {
			t.Fatal("no watchdog armed for a live drag")
		}
		// Fire it now rather than waiting out dragStallTimeout.
		st.drag.mu.Lock()
		st.drag.timer.Reset(time.Millisecond)
		st.drag.mu.Unlock()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			got := b.got()
			if len(got) > 0 && got[len(got)-1] == "up" {
				return // released, as it must be
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("button never released; phases = %v", b.got())
	})

	t.Run("a drag that ends properly is not released twice", func(t *testing.T) {
		b, st := newDragging(), &inputState{}
		applyWindowInput(b, st, windowInput{ID: "w1", Kind: "drag", Phase: "begin"}, nil)
		applyWindowInput(b, st, windowInput{ID: "w1", Kind: "drag", Phase: "end"}, nil)
		st.drag.mu.Lock()
		stillArmed := st.drag.timer != nil
		st.drag.mu.Unlock()
		if stillArmed {
			t.Fatal("watchdog left armed after a clean end")
		}
		time.Sleep(50 * time.Millisecond)
		ups := 0
		for _, p := range b.got() {
			if p == "up" {
				ups++
			}
		}
		if ups != 1 {
			t.Fatalf("got %d releases, want exactly 1", ups)
		}
		got := b.got()
		if len(got) < 3 || got[len(got)-2] != "move" || got[len(got)-1] != "up" {
			t.Fatalf("clean end phases = %v, want a final move before up", got)
		}
	})
}

// A stream must end once every viewer says it is watching nothing — and must
// NOT end on silence. The ack-idle timeout cannot cover this: it is evaluated
// only inside the in-flight gate, which a quiet window never enters because its
// sequence never advances. Popping a window out relies on the stream surviving
// without a stop, so a popup that never arrives would otherwise leave a capture
// helper running, billing the relay and holding the display awake.
func TestUnwatchedStreamIsReaped(t *testing.T) {
	// viewerCount with no capability records at all: nobody has said anything,
	// so every viewer is assumed to want frames.
	silent := &Agent{}
	silent.viewerCount = 2
	s := &winStream{a: silent}
	if !s.watched() {
		t.Fatal("reaped a stream although no viewer had reported anything")
	}
	s.noWatcherSince = time.Now().Add(-streamNoWatcherTimeout - time.Second)
	if !s.watched() {
		t.Fatal("silence must never reap a stream, however long it lasts")
	}

	// Now both viewers actively report zero panes.
	zero := 0
	watching := &Agent{}
	watching.viewerCount = 2
	watching.noteViewerCap("v1", true, &zero)
	watching.noteViewerCap("v2", true, &zero)
	s2 := &winStream{a: watching}
	if got := watching.framesWantedBy(); got != 0 {
		t.Fatalf("framesWantedBy = %d, want 0 when every viewer reports no panes", got)
	}
	if !s2.watched() {
		t.Fatal("reaped immediately; a pane opened just before its report lands would be cut off")
	}
	s2.noWatcherSince = time.Now().Add(-streamNoWatcherTimeout - time.Second)
	if s2.watched() {
		t.Fatal("stream outlived every viewer reporting no panes")
	}

	// An unknown viewer count must never reap. Zero means the relay has not
	// reported yet (or resetViewerSize just cleared it between connections);
	// a real zero already stops every stream through the count transition, so
	// reading it as "nobody is watching" would kill live panes on reconnect.
	unknown := &Agent{}
	unknown.viewerCount = 0
	s3 := &winStream{a: unknown, noWatcherSince: time.Now().Add(-streamNoWatcherTimeout - time.Second)}
	if !s3.watched() {
		t.Fatal("reaped a stream on an unknown viewer count")
	}
	if !s3.noWatcherSince.IsZero() {
		t.Fatal("countdown left running while the viewer count is unknown")
	}

	// One viewer opens a pane again: the countdown must reset, not resume.
	one := 1
	watching.noteViewerCap("v2", true, &one)
	if !s2.watched() {
		t.Fatal("a viewer with a pane open did not keep the stream alive")
	}
	if !s2.noWatcherSince.IsZero() {
		t.Fatal("countdown not reset — the stream would die mid-view")
	}
}

// slowBackend models what resolving a window actually costs: an osascript that
// enumerates every window on the system, measured at ~114ms.
type slowBackend struct {
	windowBackend
	delay time.Duration
	mu    sync.Mutex // the probe reads this off the stream goroutine
	wins  []winInfo
}

func (b *slowBackend) list() ([]winInfo, error) {
	time.Sleep(b.delay)
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]winInfo(nil), b.wins...), nil
}

func (b *slowBackend) setWins(w []winInfo) {
	b.mu.Lock()
	b.wins = w
	b.mu.Unlock()
}

// The geometry probe must not run on the capture loop. Inline, it stalled every
// window mirror for the length of an osascript every couple of seconds — at
// 60fps a visible handful of frames dropped on a schedule, and on the desktop
// view just as much as on a window.
func TestGeometryProbeDoesNotStallTheStream(t *testing.T) {
	b := &slowBackend{delay: 150 * time.Millisecond}
	b.setWins([]winInfo{{ID: "w1", W: 800, H: 600}})
	s := &winStream{a: &Agent{}, b: b, w: winInfo{ID: "w1", W: 800, H: 600}, capNative: true}

	// A probe is due immediately (lastGeoCheck is zero), so this call is the
	// one that would have paid for it.
	start := time.Now()
	if !s.checkWindow(nil, false) {
		t.Fatal("stream ended on a healthy window")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("capture loop blocked %v on the probe; it must not wait at all", elapsed)
	}

	// Further iterations while it is still out must also not wait.
	for i := 0; i < 5; i++ {
		start = time.Now()
		s.checkWindow(nil, false)
		if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
			t.Fatalf("iteration %d blocked %v while a probe was in flight", i, elapsed)
		}
	}

	// And the answer must actually be collected once it lands.
	b.setWins([]winInfo{{ID: "w1", W: 1024, H: 768}}) // resized meanwhile
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.checkWindow(nil, false)
		if !s.geoBusy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.geoBusy {
		t.Fatal("probe result was never collected")
	}
	if s.w.W != 800 && s.w.W != 1024 {
		t.Fatalf("geometry not applied: %dx%d", s.w.W, s.w.H)
	}
}

// The viewer is a remote party whose messages the relay only forwards, and the
// work a field commands is not proportional to its own size: every point of a
// phased drag is a separate injection, and a typed run becomes a script
// argument. Sizes therefore cannot be taken on trust.
func TestViewerSuppliedSizesAreBounded(t *testing.T) {
	t.Run("a phased drag cannot demand unbounded injections", func(t *testing.T) {
		winLookupMu.Lock()
		winLookupCache = map[string]winLookupEntry{}
		winLookupMu.Unlock()
		b := &draggingBackend{}
		b.wins = []winInfo{{ID: "w1", W: 800, H: 600}}
		huge := make([][2]float64, 100000)
		applyWindowInput(b, &inputState{}, windowInput{ID: "w1", Kind: "drag", Phase: "move", Path: huge}, nil)
		if n := len(b.got()); n > winMaxDragPoints {
			t.Fatalf("injected %d times for one event, cap is %d", n, winMaxDragPoints)
		}
		if len(b.got()) == 0 {
			t.Fatal("clamped away entirely; a real gesture must still land")
		}
	})

	t.Run("a typed run is clamped without splitting a character", func(t *testing.T) {
		// Multi-byte throughout: a byte-wise cut would produce invalid UTF-8.
		long := strings.Repeat("世", winMaxTypeRunes+500)
		got := clampText(long, winMaxTypeRunes)
		if len([]rune(got)) != winMaxTypeRunes {
			t.Fatalf("clamped to %d runes, want %d", len([]rune(got)), winMaxTypeRunes)
		}
		if !utf8.ValidString(got) {
			t.Fatal("clamping produced invalid UTF-8")
		}
		// Text within the bound is returned untouched.
		if s := "hello"; clampText(s, winMaxTypeRunes) != s {
			t.Fatal("clamped a string that was already short enough")
		}
	})

	t.Run("click count is clamped to what a click-state can mean", func(t *testing.T) {
		winLookupMu.Lock()
		winLookupCache = map[string]winLookupEntry{}
		winLookupMu.Unlock()
		b := newRecording()
		applyWindowInput(b, &inputState{}, windowInput{ID: "w1", Kind: "click", Count: 1 << 30}, nil)
		joined := strings.Join(b.calls, " ")
		if !strings.Contains(joined, fmt.Sprintf("click(n=%d", winMaxClickCount)) {
			t.Fatalf("calls = %q, want the count clamped to %d", joined, winMaxClickCount)
		}
	})
}

// Three intervals govern the pane-count accounting, and they are only correct
// in relation to each other. Tuning one alone has already broken it once, so
// the relationships are asserted rather than left in a comment.
func TestPaneCountIntervalsAreOrdered(t *testing.T) {
	// A record must survive several missed announcements. At one interval it
	// would expire on a single lost message; the announcements cross a relay
	// and are not guaranteed.
	if viewerCapTTL < 3*winCapAnnounceInterval {
		t.Fatalf("record lifetime %v spans fewer than three announcements of %v — one lost message expires a live record",
			viewerCapTTL, winCapAnnounceInterval)
	}

	// The reaper must outlast the record. A viewer that goes away leaves its
	// last "no panes" behind, and that ghost subtracts from the count of
	// viewers wanting frames exactly as a live one does — so a reaper shorter
	// than the record's life can finish counting down and stop a stream
	// somebody is still watching. The record expiring first prevents it.
	if streamNoWatcherTimeout <= viewerCapTTL {
		t.Fatalf("reaper %v does not outlast the record lifetime %v — a departed viewer's record could reap a live stream",
			streamNoWatcherTimeout, viewerCapTTL)
	}

	// And the reaper must still be short enough to matter: a stream nobody can
	// see holds a capture helper, bills the relay for heartbeats and keeps the
	// display awake.
	if streamNoWatcherTimeout > 5*time.Minute {
		t.Fatalf("reaper %v is too slow to collect an abandoned stream", streamNoWatcherTimeout)
	}
}

// A viewer reaches hosts older than itself: the page is served from the relay
// and updates the moment it is deployed, while agents upgrade whenever their
// owners get round to it. Anything the viewer repeats must therefore be inert
// on a host that does not understand it.
func TestHostAdvertisesWhatARepeatedHelloCosts(t *testing.T) {
	h := gatherHostInfo()
	if !h.CapsOnly {
		t.Fatal("host does not advertise caps_only, so viewers will not refresh their pane count against it")
	}

	// And the flag must mean what the viewer relies on: recorded, then dropped
	// without building a peer connection.
	a := &Agent{}
	panes := 0
	a.noteViewerCap("v1", true, &panes)
	if got := a.idleViewerCount(); got != 1 {
		t.Fatalf("idle viewers = %d, want the caps-only report recorded", got)
	}
}

// A window has one stream shared by everyone watching it, so a stop has to say
// who is stopping. Without that, two people on the same window meant either one
// of them closing a pane froze the other's picture until its own stall
// detection re-asked — a still image for about ten seconds with nothing to say
// why.
func TestStopOnlyEndsAStreamNobodyElseWants(t *testing.T) {
	a := &Agent{}

	t.Run("another watcher keeps it alive", func(t *testing.T) {
		a.winSubs = nil
		a.addWindowSub("w1", "viewerA")
		a.addWindowSub("w1", "viewerB")
		if keep := a.dropWindowSub("w1", "viewerB"); !keep {
			t.Fatal("stream ended while viewerA was still watching")
		}
		if keep := a.dropWindowSub("w1", "viewerA"); keep {
			t.Fatal("stream kept alive after the last watcher left")
		}
	})

	t.Run("the same viewer stopping twice does not strand it", func(t *testing.T) {
		a.winSubs = nil
		a.addWindowSub("w1", "viewerA")
		a.dropWindowSub("w1", "viewerA")
		if keep := a.dropWindowSub("w1", "viewerA"); keep {
			t.Fatal("a repeated stop reported a watcher that had already gone")
		}
	})

	t.Run("windows are tracked apart", func(t *testing.T) {
		a.winSubs = nil
		a.addWindowSub("w1", "viewerA")
		a.addWindowSub("w2", "viewerA")
		if keep := a.dropWindowSub("w1", "viewerA"); keep {
			t.Fatal("closing w1 was held open by an interest in w2")
		}
		if _, still := a.winSubs["w2"]; !still {
			t.Fatal("closing w1 forgot who wanted w2")
		}
	})

	t.Run("a viewer too old to identify itself keeps the old behaviour", func(t *testing.T) {
		a.winSubs = nil
		a.addWindowSub("w1", "") // records nothing
		if keep := a.dropWindowSub("w1", ""); keep {
			t.Fatal("an unattributable stop must be taken at face value")
		}
		// And it must not be able to strand a stream that others do want:
		// nothing can be attributed, so the set is cleared with it.
		a.addWindowSub("w1", "viewerA")
		if keep := a.dropWindowSub("w1", ""); keep {
			t.Fatal("an unattributable stop left the stream running")
		}
		if _, still := a.winSubs["w1"]; still {
			t.Fatal("subscriber set survived an unattributable stop")
		}
	})
}
