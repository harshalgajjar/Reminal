package main

import (
	"strings"
	"testing"
)

func TestSpeakToolIsListedAndExplainsTheVoiceMarker(t *testing.T) {
	var found map[string]any
	for _, tl := range mcpToolList() {
		if tl["name"] == "speak" {
			found = tl
		}
	}
	if found == nil {
		t.Fatal("speak is not listed")
	}
	d, _ := found["description"].(string)
	for _, want := range []string{`"[voice]"`, `"[voice, continues]"`, "on screen", "talked over", "approval"} {
		if !strings.Contains(d, want) {
			t.Errorf("speak's description does not say %q", want)
		}
	}
	if !strings.Contains(mcpInstructions, `"[voice]"`) || !strings.Contains(mcpInstructions, `"[voice, continues]"`) {
		t.Error("the server's instructions do not say what [voice] means")
	}
}

func TestSpeakReportSaysWhatCameOfIt(t *testing.T) {
	for _, c := range []struct{ reply, want string }{
		{"spoken", "Spoken."},
		{"not-listening", "no one is listening"},
		{"cut Your tests passed and", `they heard up to: "Your tests passed and"`},
		{"cut", "talked over you. Wait"},
		{"too-much", "said a lot in the last minute"},
		{"empty", "empty"},
	} {
		if got := speakReport(c.reply); !strings.Contains(got, c.want) {
			t.Errorf("speakReport(%q) = %q, want it to say %q", c.reply, got, c.want)
		}
	}
}

func TestSpeakOutsideASessionSaysSo(t *testing.T) {
	t.Setenv("REMINAL_SESSION", "")
	t.Setenv("HOME", t.TempDir())
	if _, err := mcpSpeak("hello"); err == nil {
		t.Fatal("speak outside a session did not fail")
	}
	if _, err := mcpSpeak("  "); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("empty text: %v", err)
	}
}

// The session is the one REMINAL_SESSION names, in upper case whatever the
// case it was set in.
func TestEnclosingSessionIDReadsTheEnvironment(t *testing.T) {
	t.Setenv("REMINAL_SESSION", "  abc123 ")
	if got := enclosingSessionID(); got != "ABC123" {
		t.Fatalf("enclosingSessionID() = %q", got)
	}
}
