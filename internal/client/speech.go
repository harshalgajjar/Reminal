package client

// Voice. A viewer with voice on hears what the agent in this session says
// with reminal's MCP speak tool, and what the person says is typed into the
// session for them, marked as said out loud:
//
//	viewer → agent  TypeSpeechIn {"kind":"listening","on":true}  every few seconds while voice is on
//	viewer → agent  TypeSpeechIn {"kind":"said","text":"..."}    typed in as "[voice] ...", then Return
//	viewer → agent  TypeSpeechIn {"kind":"part","text":"..."}    part of long talk still going on: "[voice, continues] ..."
//	viewer → agent  TypeSpeechIn {"kind":"cut","text":"..."}     talked over it; text = what was heard
//	agent → viewer  TypeSpeech   {"text":"..."}                  say this
//	agent → viewer  TypeSpeech   {"notice":"asking"}             what they said was not typed in, and why
//
// Said words are typed only where they can do nothing but reach an agent as
// a message (speech_gate.go): never at a permission prompt, into a shell, or
// at a password prompt.
//
// `reminal mcp`'s speak tool reaches this process over the control socket
// ("speak <base64>") and is told whether it was spoken (the Speak* replies
// below): not when no viewer is listening, and not after the person talked
// over the agent until they have said what they wanted (their next words).
// Recognising speech and the voice itself run in the viewer, on the person's
// device; only text comes here.

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"reminal/internal/protocol"
)

// The kinds of TypeSpeechIn message (the viewer's voiceui.js sends them).
const (
	speechListening = "listening"
	speechSaid      = "said"
	speechPart      = "part"
	speechCut       = "cut"
)

// What the control verb "speak" answers, for `reminal mcp` to put into words
// (cmd/reminal/mcp_speak.go). SpeakCut is followed by a space and what the
// person had heard when they talked over it, when that is known.
const (
	SpeakSpoken       = "spoken"
	SpeakNotListening = "not-listening"
	SpeakCut          = "cut"
	SpeakTooMuch      = "too-much"
	SpeakEmpty        = "empty"
)

// speechMaxPerMinute bounds what an agent can say: one stuck in a loop
// calling speak is told, not read out without end.
const speechMaxPerMinute = 30

// speechListenFor is how long one "listening" keeps voice on: viewers send it
// more often than this, so voice goes off soon after the last one leaves.
const speechListenFor = 8 * time.Second

// What is typed before words said out loud: a finished thought, and a part
// of a long one still being said (sent every so often so the agent can
// follow along, and stop the person if they asked it to).
const (
	speechMarkSaid = "[voice] "
	speechMarkPart = "[voice, continues] "
)

// Caps, in characters: one "said" typed in, one line to say, and what is kept
// of what the person had heard when they cut the agent off. Speech, not
// documents.
const (
	speechMaxSaid   = 2000
	speechMaxSpoken = 1000
	speechMaxHeard  = 120
)

// speechState is one session's voice: whether a viewer listens, and whether
// the person talked over the agent (and what they had heard by then).
type speechState struct {
	mu          sync.Mutex
	listenUntil time.Time
	cut         bool
	heard       string
	spokenAt    []time.Time // when recent lines were said, for speechMaxPerMinute

	now      func() time.Time   // tests: the clock (nil: time.Now)
	typeKeys func([]byte) error // tests: what would be typed (nil: into the PTY)
	blocked  func() string      // tests: what is in front (nil: speechBlocked reads it)
}

func (s *speechState) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// listen keeps voice on for speechListenFor from now, or turns it off.
func (s *speechState) listen(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		s.listenUntil = s.clock().Add(speechListenFor)
	} else {
		s.listenUntil = time.Time{}
	}
}

// talkedOver notes that the person cut the agent off, having heard heard.
func (s *speechState) talkedOver(heard string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cut, s.heard = true, lastRunes(speechText(heard, 1<<16), speechMaxHeard)
}

// spokeAgain notes the person's next words: the agent may speak again, and
// someone is plainly listening.
func (s *speechState) spokeAgain() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cut, s.heard = false, ""
	s.listenUntil = s.clock().Add(speechListenFor)
}

// maySpeak is what speak would answer if it did not send: SpeakSpoken when
// it may.
func (s *speechState) maySpeak() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.clock().Before(s.listenUntil):
		return SpeakNotListening
	case s.cut:
		return SpeakCut + " " + s.heard
	}
	return SpeakSpoken
}

// allowLine counts a line said, false when speechMaxPerMinute were said in
// the last minute already (that one is not counted).
func (s *speechState) allowLine() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.clock()
	keep := s.spokenAt[:0]
	for _, at := range s.spokenAt {
		if t.Sub(at) < time.Minute {
			keep = append(keep, at)
		}
	}
	s.spokenAt = keep
	if len(keep) >= speechMaxPerMinute {
		return false
	}
	s.spokenAt = append(s.spokenAt, t)
	return true
}

// speechText keeps text a voice can say or a terminal can take: no control
// characters (a "said" must never carry a key a terminal would act on), runs
// of space made one, trimmed and capped at max characters.
func speechText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// lastRunes keeps the end of s, at most max characters, marked "..." when
// cut: where the person stopped the agent is the part worth keeping.
func lastRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return "..." + string(r[len(r)-max:])
	}
	return s
}

// handleSpeechIn acts on a viewer's voice message.
func (a *Agent) handleSpeechIn(data string) {
	if len(data) == 0 {
		return
	}
	raw, err := a.box.Decrypt(data)
	if err != nil {
		return
	}
	var in struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
		On   bool   `json:"on"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return
	}
	switch in.Kind {
	case speechListening:
		a.speech.listen(in.On)
	case speechCut:
		a.speech.talkedOver(in.Text)
	case speechSaid, speechPart:
		text := speechText(in.Text, speechMaxSaid)
		if text == "" {
			return
		}
		a.speech.spokeAgain()
		// The marker tells the agent this was said out loud, so it answers
		// with speak (the MCP server's instructions say what it means); a
		// part of a long stretch of talk that is still going on is marked so,
		// and is not to be answered.
		marker := speechMarkSaid
		if in.Kind == speechPart {
			marker = speechMarkPart
		}
		a.typeSpoken(marker + text)
	}
}

// typeSpoken types what was said into the session, then Return on its own
// once the text has settled, as send_keys does: inside a bracketed paste a
// Return would be a newline, not Enter. Only where the words can do nothing
// but reach an agent (speechBlocked), asked twice: before the words, and
// before the Return, which a prompt drawn in between would take as its
// answer. The viewers are told when the words were not typed, or not sent.
func (a *Agent) typeSpoken(line string) {
	body, tail, err := PrepareInjectKeysSplit(line, true)
	if err != nil {
		return
	}
	typ := a.injectKeys
	if a.speech.typeKeys != nil {
		typ = a.speech.typeKeys
	}
	go func() {
		a.awaitTyped()
		if why := a.speechBlocked(); why != "" {
			a.speechNotice(why)
			return
		}
		if typ(body) != nil || tail == nil {
			return
		}
		time.Sleep(EnterSettle)
		if a.speechBlocked() != "" {
			a.speechNotice(speechUnsent) // the words are in the box, not sent
			return
		}
		_ = typ(tail)
	}()
}

// speak says text to the viewers listening by voice, and reports what came
// of it (one of the Speak* replies), for the speak tool to pass on.
func (a *Agent) speak(text string) (string, error) {
	text = speechText(text, speechMaxSpoken)
	if text == "" {
		return SpeakEmpty, nil
	}
	if status := a.speech.maySpeak(); status != SpeakSpoken {
		return status, nil
	}
	if !a.speech.allowLine() {
		return SpeakTooMuch, nil
	}
	if err := a.toListeners(speechOut{Text: text}); err != nil {
		return "", err
	}
	return SpeakSpoken, nil
}

// speechOut is what TypeSpeech carries: a line to say, or a notice that
// words said were not typed in, and why (speechBlocked's reasons).
type speechOut struct {
	Text   string `json:"text,omitempty"`
	Notice string `json:"notice,omitempty"`
}

// toListeners sends one TypeSpeech message to the viewers.
func (a *Agent) toListeners(out speechOut) error {
	payload, err := json.Marshal(out)
	if err != nil {
		return err
	}
	enc, err := a.box.Encrypt(payload)
	if err != nil {
		return err
	}
	return a.toViewers(protocol.Message{Type: protocol.TypeSpeech, Data: enc})
}

// speechNotice tells the viewers words they said were not typed in.
func (a *Agent) speechNotice(why string) { _ = a.toListeners(speechOut{Notice: why}) }

// handleSpeakControl is the control verb: "speak <base64 of the text>".
func (a *Agent) handleSpeakControl(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", err
	}
	return a.speak(string(raw))
}
