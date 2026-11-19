package client

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// Agent at its prompt, a screen showing it: words go in.
func gateAgent(t *testing.T, fg, state, screen string) *Agent {
	t.Helper()
	a, _, _ := speechAgent(t)
	a.speech.blocked = nil // the real check
	a.attnFG, a.attnState = fg, state
	a.screen = testScreen(t, 80, 24)
	_, _ = a.screen.Write([]byte(screen))
	return a
}

const claudeApprovalScreen = "\r\n Bash command\r\n   rm -rf build\r\n\r\n Do you want to proceed?\r\n ❯ 1. Yes\r\n   2. No, and tell Claude what to do differently (esc)\r\n"

func TestSpeechBlockedReadsWhatIsInFront(t *testing.T) {
	for _, c := range []struct {
		name, fg, state, screen, want string
	}{
		{"agent at its prompt", "claude", "done", "> \r\n", ""},
		{"agent working", "claude", "working", "✻ Thinking… (esc to interrupt)\r\n", ""},
		{"permission prompt", "claude", "input", claudeApprovalScreen, speechAsking},
		// Drawn a moment ago: the state has not settled yet, the screen says it.
		{"permission prompt not settled", "claude", "working", claudeApprovalScreen, speechAsking},
		{"bare shell", "zsh", "", "$ \r\n", speechNoAgent},
		{"login shell", "-zsh", "", "$ \r\n", speechNoAgent},
		{"another program", "vim", "done", "~\r\n", speechNoAgent},
		{"agent exited, state left over", "bash", "done", "$ \r\n", speechNoAgent},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := gateAgent(t, c.fg, c.state, c.screen).speechBlocked(); got != c.want {
				t.Fatalf("speechBlocked = %q, want %q", got, c.want)
			}
		})
	}
}

// Words said while a permission prompt is up are not typed, no Return is
// sent, and the viewer is told why.
func TestSpeechSaidAtAPermissionPromptTypesNothing(t *testing.T) {
	for _, kind := range []string{"said", "part"} {
		a := gateAgent(t, "claude", "input", claudeApprovalScreen)
		var typed []string
		a.speech.typeKeys = func(b []byte) error { typed = append(typed, string(b)); return nil }
		speechIn(t, a, map[string]any{"kind": kind, "text": "yes 1"})
		time.Sleep(EnterSettle + 300*time.Millisecond)
		if len(typed) != 0 {
			t.Fatalf("%s: typed %q at a permission prompt", kind, typed)
		}
	}
}

// A prompt drawn after the words went in, before the Return: no Return.
func TestSpeechReturnHeldWhenAPromptAppears(t *testing.T) {
	a, typed, mu := speechAgent(t)
	calls := 0
	a.speech.blocked = func() string {
		calls++
		if calls > 1 {
			return speechAsking
		}
		return ""
	}
	speechIn(t, a, map[string]any{"kind": "said", "text": "and then"})
	time.Sleep(EnterSettle + 300*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(*typed) != 1 || (*typed)[0] != "[voice] and then" {
		t.Fatalf("typed %q, want the words and no Return", *typed)
	}
}

func TestSpeechNoticeSaysWhy(t *testing.T) {
	b, _ := json.Marshal(speechOut{Notice: speechAsking})
	if string(b) != `{"notice":"asking"}` {
		t.Fatalf("notice payload %s", b)
	}
}

// An agent stuck calling speak is told, not read out without end.
func TestSpeakIsBounded(t *testing.T) {
	a, _, _ := speechAgent(t)
	now := time.Unix(1000, 0)
	a.speech.now = func() time.Time { return now }
	speechIn(t, a, map[string]any{"kind": "listening", "on": true})
	for i := 0; i < speechMaxPerMinute; i++ {
		if got, _ := a.speak("again"); got == "too-much" {
			t.Fatalf("line %d refused", i)
		}
	}
	if got, _ := a.speak("again"); got != "too-much" {
		t.Fatalf("line past the bound: %q", got)
	}
	now = now.Add(time.Minute)
	speechIn(t, a, map[string]any{"kind": "listening", "on": true})
	if got, _ := a.speak("again"); got == "too-much" {
		t.Fatal("still refused a minute later")
	}
}

// A transcribed "1", "2" or "y" as the whole utterance is exactly what a
// permission prompt takes as its answer: at one, nothing is typed, for a
// part as for a whole thought.
func TestSpeechLoneOptionKeyAtAPromptTypesNothing(t *testing.T) {
	for _, kind := range []string{"said", "part"} {
		for _, text := range []string{"1", "2", "y", "Yes.", "1."} {
			a := gateAgent(t, "claude", "input", claudeApprovalScreen)
			var mu sync.Mutex
			var typed []string
			a.speech.typeKeys = func(b []byte) error { mu.Lock(); typed = append(typed, string(b)); mu.Unlock(); return nil }
			speechIn(t, a, map[string]any{"kind": kind, "text": text})
			time.Sleep(EnterSettle + 200*time.Millisecond)
			mu.Lock()
			if len(typed) != 0 {
				t.Errorf("%s %q: typed %q at a permission prompt", kind, text, typed)
			}
			mu.Unlock()
		}
	}
}

// A fresh agent, or one in a session a restarted reminal has just taken
// over: the probe has not said what is in front yet. It is not refused as
// "no agent" when the probe, asked to look, finds one.
func TestSpeechWaitsForTheProbesFirstLook(t *testing.T) {
	a := gateAgent(t, "", "", "> \r\n")
	go func() {
		time.Sleep(200 * time.Millisecond)
		a.metaMu.Lock()
		a.attnFG, a.attnState = "claude", "done"
		a.metaMu.Unlock()
	}()
	if got := a.speechBlocked(); got != "" {
		t.Fatalf("speechBlocked = %q for an agent the probe found a moment later", got)
	}
	// Still nothing in front after the wait: refused.
	b := gateAgent(t, "", "", "$ \r\n")
	if got := b.speechBlocked(); got != speechNoAgent {
		t.Fatalf("speechBlocked = %q with nothing found, want %q", got, speechNoAgent)
	}
}

// OpenCode and Kilo draw a dialog (a permission prompt, a picker, a menu)
// over their input box, in words no cue knows; for them words go in only
// while the box's own mark is on screen.
func TestSpeechTypesIntoOpenCodeOnlyAtItsBox(t *testing.T) {
	const box = "\r\n  ┃  Ask anything\r\n  ┃\r\n  ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\r\n   enter send\r\n"
	const permission = "\r\n  △ Permission required\r\n    ← Access external directory /tmp\r\n    Patterns - /tmp/*\r\n\r\n   Allow once   Allow always   Reject\r\n   ⇆ select  enter confirm\r\n"
	for _, prog := range []string{"opencode", "kilo"} {
		if got := gateAgent(t, prog, "done", box).speechBlocked(); got != "" {
			t.Errorf("%s at its box: speechBlocked = %q, want it allowed", prog, got)
		}
		if got := gateAgent(t, prog, "working", permission).speechBlocked(); got != speechAsking {
			t.Errorf("%s at a permission prompt: speechBlocked = %q, want %q", prog, got, speechAsking)
		}
	}
}

// No screen to read: an agent whose box has a mark is not typed into (its
// box cannot be seen); one without a mark rests on its state, as before.
func TestSpeechWithNoScreenFailsClosedForMarkedAgents(t *testing.T) {
	a, _, _ := speechAgent(t)
	a.speech.blocked = nil
	a.attnFG, a.attnState = "opencode", "done"
	if got := a.speechBlocked(); got != speechAsking {
		t.Errorf("opencode, no screen: %q, want %q", got, speechAsking)
	}
	a.attnFG = "claude"
	if got := a.speechBlocked(); got != "" {
		t.Errorf("claude, no screen, done: %q, want it allowed", got)
	}
}
