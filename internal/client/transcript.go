// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxTranscriptHits  = 8
	snippetPad         = 48
	searchControlWait  = 3 * time.Second
	transcriptWait     = 8 * time.Second
	maxTranscriptBytes = 192 * 1024 // tail; stays under the 1 MiB directory frame
)

// SearchAgentTranscript asks a local agent to regex-search its live
// scrollback (the same buffer a joining viewer replays). The agent strips
// ANSI first. Empty / no-match returns a nil slice, not an error.
func SearchAgentTranscript(pid int, pattern string) ([]string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("search pattern is empty")
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}
	payload, err := sendControlToDeadline(pid, "search "+pattern, searchControlWait)
	if err != nil {
		return nil, err
	}
	if payload == "" {
		return nil, nil
	}
	var resp struct {
		Hits []string `json:"hits"`
	}
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		return nil, fmt.Errorf("agent search reply: %w", err)
	}
	return resp.Hits, nil
}

// ReadAgentTranscript asks a local agent for its stripped scrollback.
// The tail is capped at maxTranscriptBytes so a dump fits a directory frame
// and an MCP result. truncated is true when older history was dropped.
func ReadAgentTranscript(pid int, timeout ...time.Duration) (string, bool, error) {
	wait := transcriptWait
	if len(timeout) > 0 && timeout[0] > 0 {
		wait = timeout[0]
	}
	payload, err := sendControlToDeadline(pid, "transcript", wait)
	if err != nil {
		return "", false, err
	}
	if payload == "" {
		return "", false, nil
	}
	var resp struct {
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		return "", false, fmt.Errorf("agent transcript reply: %w", err)
	}
	return resp.Text, resp.Truncated, nil
}

func (a *Agent) handleTranscriptControl() (string, error) {
	text, truncated := clipTranscript(a.plaintextTranscript(), maxTranscriptBytes)
	body, err := json.Marshal(struct {
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	}{Text: text, Truncated: truncated})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (a *Agent) handleSearchControl(pattern string) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", fmt.Errorf("search pattern is empty")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex: %w", err)
	}
	hits := transcriptHits(a.plaintextTranscript(), re)
	body, err := json.Marshal(struct {
		Hits []string `json:"hits"`
	}{Hits: hits})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// plaintextTranscript decrypts the live buffer and strips terminal chrome so
// a regex can match the text a person would read off the screen.
func (a *Agent) plaintextTranscript() string {
	if a == nil || a.buf == nil || a.box == nil {
		return ""
	}
	var b strings.Builder
	for _, e := range a.buf.From(0) {
		if e.Data == "" {
			continue
		}
		pt, err := a.box.Decrypt(e.Data)
		if err != nil {
			continue
		}
		b.Write(pt)
	}
	return stripANSI(b.String())
}

func transcriptHits(text string, re *regexp.Regexp) []string {
	if text == "" || re == nil {
		return nil
	}
	locs := re.FindAllStringIndex(text, maxTranscriptHits)
	if len(locs) == 0 {
		return nil
	}
	out := make([]string, 0, len(locs))
	for _, loc := range locs {
		start := loc[0] - snippetPad
		if start < 0 {
			start = 0
		}
		end := loc[1] + snippetPad
		if end > len(text) {
			end = len(text)
		}
		start = runeFloor(text, start)
		end = runeCeil(text, end)
		snip := collapseWS(text[start:end])
		if start > 0 {
			snip = "…" + snip
		}
		if end < len(text) {
			snip += "…"
		}
		out = append(out, snip)
	}
	return out
}

func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func runeFloor(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

func runeCeil(s string, i int) int {
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// clipTranscript keeps the newest max bytes (rune-aligned). truncated reports
// that older history was dropped.
func clipTranscript(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	s = s[len(s)-max:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s, true
}

// stripANSI removes CSI / OSC / other ESC sequences and CRs so scrollback
// can be searched as the text on screen, not the paint protocol.
func stripANSI(s string) string {
	in := []byte(s)
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c == '\r' {
			continue
		}
		if c != 0x1b {
			out = append(out, c)
			continue
		}
		if i+1 >= len(in) {
			break
		}
		switch in[i+1] {
		case '[': // CSI
			i += 2
			for i < len(in) {
				if in[i] >= 0x40 && in[i] <= 0x7e {
					break
				}
				i++
			}
		case ']': // OSC … BEL or ST
			i += 2
			for i < len(in) {
				if in[i] == 0x07 {
					break
				}
				if in[i] == 0x1b && i+1 < len(in) && in[i+1] == '\\' {
					i++
					break
				}
				i++
			}
		case 'P', 'X', '^', '_': // DCS / SOS / PM / APC
			i += 2
			for i < len(in) {
				if in[i] == 0x1b && i+1 < len(in) && in[i+1] == '\\' {
					i++
					break
				}
				i++
			}
		default:
			i++ // ESC + the following byte
		}
	}
	return string(out)
}
