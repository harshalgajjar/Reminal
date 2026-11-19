package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"reminal/internal/client"
	"reminal/internal/session"
)

// speakTool is what the agent says out loud to a person talking to it by
// voice in reminal (internal/client/speech.go). What they say reaches it as a
// line starting with "[voice]"; the description repeats the rules, since some
// harnesses never show the server's instructions.
func speakTool(obj func(map[string]any, ...string) map[string]any, str func(string) map[string]any) map[string]any {
	return map[string]any{
		"name": "speak",
		"description": "Say something out loud to the person talking to you by voice in reminal: their messages start with \"[voice]\". " +
			"Answer those with speak, in one or two short plain sentences, as you would say them to someone in the room. " +
			"No markdown, code, file paths or URLs in what you speak: name things in words. " +
			"Before a task that takes more than a few seconds, say what you are about to do; when it is done, say the outcome. " +
			"Details, code and anything long stay in your reply on screen: say that they are there. " +
			"A message starting with \"[voice, continues]\" is part of what they are still saying (it comes every few seconds while they talk at length): do not answer it; answer once the closing \"[voice]\" message comes. You can cut in on long talk: speaking in answer to a part is heard at once, over them; do it only if they asked you to (\"stop me if...\"), and then briefly; they first hear \"Sorry,\" and the rest only if they stop for it, and if they talk on your next message says so. " +
			"If it answers that you were talked over, stop and wait for what they say next; do not repeat it. " +
			"Never ask for or accept an approval or a password by voice: ask for those on screen. " +
			"Works only inside a reminal session.",
		"inputSchema": obj(map[string]any{
			"text": str("What to say, as plain spoken sentences."),
		}, "text"),
	}
}

// speakControlWait is how long the session is given to answer: it only
// encrypts and sends a line.
const speakControlWait = 5 * time.Second

// mcpSpeak hands text to this session's process, which says it to the viewers
// listening by voice, and tells the agent what came of it.
func mcpSpeak(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.New("text is required")
	}
	id := enclosingSessionID()
	if id == "" {
		return "", errors.New("speak works only inside a reminal session: reply on screen")
	}
	rec, err := session.ReadActiveByID(id)
	if err != nil || rec.PID <= 0 {
		return "", fmt.Errorf("this reminal session (%s) is not running: reply on screen", id)
	}
	reply, err := sendControl(rec.PID, "speak "+base64.StdEncoding.EncodeToString([]byte(text)), speakControlWait)
	if err != nil {
		// A session started by a reminal from before voice has no speak.
		return "Not spoken: this session's reminal has no voice yet (it was started before an update). Reply on screen only.", nil
	}
	return speakReport(reply), nil
}

// speakReport turns the session's answer (client.Speak*) into what the agent
// is told.
func speakReport(reply string) string {
	const wait = "Wait for what they say next; do not repeat it."
	switch {
	case reply == client.SpeakSpoken:
		return "Spoken."
	case reply == client.SpeakNotListening:
		return "Not spoken: no one is listening by voice right now. Reply on screen only."
	case reply == client.SpeakCut || strings.HasPrefix(reply, client.SpeakCut+" "):
		if heard := strings.TrimSpace(strings.TrimPrefix(reply, client.SpeakCut)); heard != "" {
			return fmt.Sprintf("Not spoken: the person talked over you (they heard up to: %q). %s", heard, wait)
		}
		return "Not spoken: the person talked over you. " + wait
	case reply == client.SpeakTooMuch:
		return "Not spoken: you have said a lot in the last minute. Say less, and reply on screen."
	case reply == client.SpeakEmpty:
		return "Nothing to say: text was empty."
	case reply == client.SpeakNoVoice || strings.HasPrefix(reply, client.SpeakNoVoice+" "):
		s := "Not spoken: the person's device has no voice to say it with right now. Reply on screen only."
		if earlier := strings.TrimSpace(strings.TrimPrefix(reply, client.SpeakNoVoice)); earlier != "" {
			s += fmt.Sprintf(" What you said before this was not heard either: %q.", earlier)
		}
		return s
	case strings.HasPrefix(reply, client.SpeakSpokenUnheard+" "):
		earlier := strings.TrimSpace(strings.TrimPrefix(reply, client.SpeakSpokenUnheard))
		return fmt.Sprintf("Spoken. But what you said before this was not heard (the person's device could not say it): %q. Say it again if it matters.", earlier)
	}
	return "Not spoken (" + reply + "). Reply on screen only."
}
