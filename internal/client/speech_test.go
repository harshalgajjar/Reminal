package client

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"reminal/internal/crypto"
)

func speechAgent(t *testing.T) (*Agent, *[]string, *sync.Mutex) {
	t.Helper()
	key := make([]byte, 32)
	box, err := crypto.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{box: box}
	var mu sync.Mutex
	typed := []string{}
	a.speech.typeKeys = func(b []byte) error { mu.Lock(); typed = append(typed, string(b)); mu.Unlock(); return nil }
	a.speech.blocked = func() string { return "" } // an agent at its prompt
	return a, &typed, &mu
}

func speechIn(t *testing.T, a *Agent, v map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(v)
	enc, err := a.box.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	a.handleSpeechIn(enc)
}

// What was said is typed in after the voice marker, and Return goes on its
// own: inside a bracketed paste it would be a newline, not Enter.
func TestSpeechSaidIsTypedWithTheMarkerThenReturn(t *testing.T) {
	a, typed, mu := speechAgent(t)
	speechIn(t, a, map[string]any{"kind": "said", "text": "list the files"})
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(*typed)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*typed) != 2 || (*typed)[0] != "[voice] list the files" || (*typed)[1] != "\r" {
		t.Fatalf("typed %q, want the marked text then a lone Return", *typed)
	}
}

// Nothing said can carry a key a terminal acts on.
func TestSpeechSaidCarriesNoControlKeys(t *testing.T) {
	a, typed, mu := speechAgent(t)
	speechIn(t, a, map[string]any{"kind": "said", "text": "rm\x03 -rf\x1b[A\r\nnow\u0085"})
	time.Sleep(EnterSettle + 300*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(*typed) == 0 {
		t.Fatal("nothing typed")
	}
	if got := (*typed)[0]; got != "[voice] rm -rf [A now" {
		t.Fatalf("typed %q", got)
	}
	for _, r := range (*typed)[0] {
		if r < 0x20 {
			t.Fatalf("control character %q typed in %q", r, (*typed)[0])
		}
	}
}

// Empty or blank speech types nothing.
func TestSpeechSaidBlankTypesNothing(t *testing.T) {
	a, typed, mu := speechAgent(t)
	speechIn(t, a, map[string]any{"kind": "said", "text": " \t\r\n "})
	time.Sleep(EnterSettle + 100*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(*typed) != 0 {
		t.Fatalf("typed %q for blank speech", *typed)
	}
}

func TestSpeakOnlyWhileSomeoneListensAndNotAfterACut(t *testing.T) {
	a, _, _ := speechAgent(t)
	now := time.Unix(1000, 0)
	a.speech.now = func() time.Time { return now }

	if got, _ := a.speak("hello"); got != "not-listening" {
		t.Fatalf("no one listening: %q", got)
	}
	speechIn(t, a, map[string]any{"kind": "listening", "on": true})
	// Listening, no viewer connection in this test: it tries to send (and
	// says why it could not), it does not report that no one listens.
	if got, err := a.speak("hello"); got == "not-listening" || (err == nil && got != "spoken") {
		t.Fatalf("listening: %q, %v", got, err)
	}

	speechIn(t, a, map[string]any{"kind": "cut", "text": "Your tests passed and"})
	if got, _ := a.speak("and one more thing"); got != "cut Your tests passed and" {
		t.Fatalf("after a cut: %q", got)
	}
	// Their next words: it may speak again.
	a.speech.typeKeys = func([]byte) error { return nil }
	speechIn(t, a, map[string]any{"kind": "said", "text": "go on"})
	if got, _ := a.speak("sure"); got == "not-listening" || strings.HasPrefix(got, "cut") {
		t.Fatalf("after their next words: %q", got)
	}

	// Listening lapses when the viewer stops saying so.
	now = now.Add(speechListenFor + time.Second)
	if got, _ := a.speak("anyone?"); got != "not-listening" {
		t.Fatalf("after listening lapsed: %q", got)
	}
	speechIn(t, a, map[string]any{"kind": "listening", "on": true})
	speechIn(t, a, map[string]any{"kind": "listening", "on": false})
	if got, _ := a.speak("anyone?"); got != "not-listening" {
		t.Fatalf("after voice off: %q", got)
	}
}

// The device's voice failed: a line it could not say, already reported
// spoken, is told to the agent on its next speak; while the viewer has no
// voice at all, speak sends nothing and says so; once it has one again,
// "Spoken." as before. A viewer from before "voice" was sent has a voice.
func TestSpeakSaysWhatTheDeviceCouldNotSay(t *testing.T) {
	a, _, _ := speechAgent(t)
	a.speech.now = func() time.Time { return time.Unix(1000, 0) }
	var sent []string
	a.speech.out = func(o speechOut) error { sent = append(sent, o.Text); return nil }

	speechIn(t, a, map[string]any{"kind": "listening", "on": true})
	if got, err := a.speak("Here is the plan."); err != nil || got != "spoken" {
		t.Fatalf("first line: %q, %v", got, err)
	}
	// The frame could not say it, and has the natural voice ready (Auto).
	speechIn(t, a, map[string]any{"kind": "unheard", "text": "Here is the plan."})
	if got, _ := a.speak("It has three steps."); got != "spoken-unheard Here is the plan." {
		t.Fatalf("after a line not heard: %q", got)
	}
	if got, _ := a.speak("First, the build."); got != "spoken" {
		t.Fatalf("told once: %q", got)
	}

	// No voice (the person picked the device's, which has none; or Auto
	// while the natural voice loads): nothing is sent.
	speechIn(t, a, map[string]any{"kind": "unheard", "text": "First, the build."})
	speechIn(t, a, map[string]any{"kind": "listening", "on": true, "voice": false})
	n := len(sent)
	if got, _ := a.speak("Second, the tests."); got != "no-voice First, the build." {
		t.Fatalf("no voice, a line not heard before: %q", got)
	}
	if got, _ := a.speak("Third."); got != "no-voice" {
		t.Fatalf("no voice: %q", got)
	}
	if len(sent) != n {
		t.Fatalf("sent with no voice: %q", sent[n:])
	}
	// A voice again.
	speechIn(t, a, map[string]any{"kind": "listening", "on": true, "voice": true})
	if got, _ := a.speak("Done."); got != "spoken" {
		t.Fatalf("a voice again: %q", got)
	}
	speechIn(t, a, map[string]any{"kind": "listening", "on": true, "voice": false})
	speechIn(t, a, map[string]any{"kind": "listening", "on": true})
	if got, _ := a.speak("Done."); got != "spoken" {
		t.Fatalf("listening without voice (an older viewer): %q", got)
	}

	// Voice off forgets lines not heard; a blank one is nothing.
	speechIn(t, a, map[string]any{"kind": "unheard", "text": "lost"})
	speechIn(t, a, map[string]any{"kind": "unheard", "text": "  "})
	speechIn(t, a, map[string]any{"kind": "listening", "on": false})
	speechIn(t, a, map[string]any{"kind": "listening", "on": true})
	if got, _ := a.speak("Hello."); got != "spoken" {
		t.Fatalf("after voice off and on: %q", got)
	}
	// What is kept of many is its end, capped.
	for i := 0; i < 40; i++ {
		speechIn(t, a, map[string]any{"kind": "unheard", "text": "one two three four five six seven eight nine ten."})
	}
	got, _ := a.speak("x")
	if u := strings.TrimPrefix(got, "spoken-unheard "); u == got || len([]rune(u)) > speechMaxUnheard+3 || !strings.HasSuffix(u, "ten.") {
		t.Fatalf("many lines not heard: %q", got)
	}
}

// What they heard is kept short: its end, which is where they stopped it.
func TestSpeechCutKeepsTheEndOfWhatWasHeard(t *testing.T) {
	a, _, _ := speechAgent(t)
	a.speech.now = func() time.Time { return time.Unix(1000, 0) }
	speechIn(t, a, map[string]any{"kind": "listening", "on": true})
	long := strings.Repeat("word ", 100) + "the end"
	speechIn(t, a, map[string]any{"kind": "cut", "text": long})
	got, _ := a.speak("x")
	if !strings.HasPrefix(got, "cut ...") || !strings.HasSuffix(got, "the end") || len([]rune(got)) > len("cut ...")+speechMaxHeard {
		t.Fatalf("cut report %q", got)
	}
}

// A message that does not decrypt with the session key does nothing.
func TestSpeechInIgnoresWhatDoesNotDecrypt(t *testing.T) {
	a, typed, mu := speechAgent(t)
	a.handleSpeechIn(`{"kind":"said","text":"ls"}`)
	time.Sleep(EnterSettle + 100*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(*typed) != 0 {
		t.Fatalf("typed %q from an unencrypted message", *typed)
	}
}

func TestSpeakControlDecodesBase64(t *testing.T) {
	a, _, _ := speechAgent(t)
	if got, err := a.handleSpeakControl("aGVsbG8="); err != nil || got != "not-listening" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := a.handleSpeakControl("%%%"); err == nil {
		t.Fatal("bad base64 accepted")
	}
}

// A part of long talk still going on is typed with its own marker, so the
// agent follows along without answering, and lets the agent speak again
// after a talk-over as their words do.
func TestSpeechPartIsTypedAsContinuing(t *testing.T) {
	a, typed, mu := speechAgent(t)
	a.speech.cut, a.speech.heard = true, "x"
	speechIn(t, a, map[string]any{"kind": "part", "text": "so the first thing is the build"})
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(*typed)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*typed) != 2 || (*typed)[0] != "[voice, continues] so the first thing is the build" || (*typed)[1] != "\r" {
		t.Fatalf("typed %q, want the continuing marker, the text, then a lone Return", *typed)
	}
	if a.speech.cut {
		t.Fatal("their words left the talk-over standing")
	}
}

// The viewer (web/voiceui.js) sends only kinds handled here, and every kind
// handled here is one it sends: a renamed kind on either side would be
// dropped without a word.
func TestSpeechKindsMatchTheViewers(t *testing.T) {
	js, err := webIndex.ReadFile("web/voiceui.js")
	if err != nil {
		t.Fatal(err)
	}
	sent := map[string]bool{}
	for _, m := range regexp.MustCompile(`kind: '([a-z_]+)'`).FindAllStringSubmatch(string(js), -1) {
		sent[m[1]] = true
	}
	handled := map[string]bool{speechListening: true, speechSaid: true, speechPart: true, speechCut: true, speechUnheard: true}
	for k := range sent {
		if !handled[k] {
			t.Errorf("voiceui.js sends kind %q, which handleSpeechIn ignores", k)
		}
	}
	for k := range handled {
		if !sent[k] {
			t.Errorf("handleSpeechIn handles kind %q, which voiceui.js never sends", k)
		}
	}
}

// host_info says this reminal takes voice: the viewer offers its mic only to
// a session whose reminal says so.
func TestHostInfoSaysVoiceIsHere(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, _, _ := speechAgent(t)
	msg, ok := a.hostInfoMsg(false)
	if !ok {
		t.Fatal("no host_info")
	}
	raw, err := a.box.Decrypt(msg.Data)
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		Voice *bool `json:"voice"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	if info.Voice == nil || !*info.Voice {
		t.Fatalf("host_info voice: %v (%s)", info.Voice, raw)
	}
}
