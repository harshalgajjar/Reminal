// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/session"
	"golang.org/x/term"
)

// settingRow is one toggle on the settings page. apply, if set, pushes the new
// value to every running agent live and returns how many it reached.
type settingRow struct {
	key   string
	label string
	desc  string
	get   func(*config.Settings) bool
	set   func(*config.Settings, bool)
	apply func(bool) int
}

func settingRows() []settingRow {
	return []settingRow{
		{
			key:   "always-unlocked",
			label: "Always keep unlocked",
			desc:  "Keep this Mac's display awake so it can't idle-lock — remote window control needs it unlocked. Costs a lit screen; can't beat a closed lid.",
			get:   func(s *config.Settings) bool { return s.StayUnlocked },
			set:   func(s *config.Settings, v bool) { s.StayUnlocked = v },
			apply: applyStayUnlocked,
		},
	}
}

// applyStayUnlocked tells every running shell agent to hold/release the display
// inhibitor now, so the toggle takes effect without restarting sessions.
func applyStayUnlocked(on bool) int {
	all, err := session.ReadAllActive()
	if err != nil {
		return 0
	}
	cmd := "stayunlock off"
	if on {
		cmd = "stayunlock on"
	}
	n := 0
	for i := range all {
		if all[i].IsPort() {
			continue
		}
		if _, err := sendControl(all[i].PID, cmd); err == nil {
			n++
		}
	}
	return n
}

// runSettings is the `reminal settings` entrypoint. With no args on a TTY it
// opens the interactive page; with `<key> on|off` it sets non-interactively;
// otherwise it prints the current values.
func runSettings(args []string) error {
	s := config.LoadSettings()
	rows := settingRows()
	if len(args) >= 1 {
		row := findRow(rows, args[0])
		if row == nil {
			return fmt.Errorf("unknown setting %q — run `reminal settings` to see them", args[0])
		}
		if len(args) < 2 {
			return fmt.Errorf("usage: reminal settings %s on|off", row.key)
		}
		on, ok := parseOnOff(args[1])
		if !ok {
			return fmt.Errorf("expected on|off, got %q", args[1])
		}
		row.set(&s, on)
		if err := config.SaveSettings(s); err != nil {
			return err
		}
		n := 0
		if row.apply != nil {
			n = row.apply(on)
		}
		fmt.Printf("%s → %s%s\n", row.label, onOffWord(on), appliedSuffix(n))
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return printSettings(rows, s)
	}
	return settingsUI(rows, s)
}

func findRow(rows []settingRow, key string) *settingRow {
	key = strings.ToLower(strings.TrimSpace(key))
	for i := range rows {
		if rows[i].key == key {
			return &rows[i]
		}
	}
	return nil
}

func parseOnOff(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1", "enable", "enabled":
		return true, true
	case "off", "false", "no", "0", "disable", "disabled":
		return false, true
	}
	return false, false
}

func onOffWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func appliedSuffix(n int) string {
	if n <= 0 {
		return ""
	}
	s := ""
	if n != 1 {
		s = "s"
	}
	return fmt.Sprintf(" (applied to %d running session%s)", n, s)
}

func printSettings(rows []settingRow, s config.Settings) error {
	fmt.Println("reminal settings:")
	for i := range rows {
		state := "off"
		if rows[i].get(&s) {
			state = "on"
		}
		fmt.Printf("  %-20s %s\n", rows[i].key, state)
	}
	fmt.Println("\n  toggle: reminal settings <name> on|off   (or run `reminal settings` in a terminal)")
	return nil
}

// settingsUI runs the interactive, alt-screen settings page. Same raw-mode
// contract as the session picker.
func settingsUI(rows []settingRow, s config.Settings) error {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return printSettings(rows, s)
	}
	defer term.Restore(fd, old)

	out := os.Stdout
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")            // alt screen, hide cursor
	defer fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")      // restore

	cursor, status := 0, ""
	drawSettings(out, rows, &s, cursor, status)
	buf := make([]byte, 16)
	for {
		n, rerr := os.Stdin.Read(buf)
		if rerr != nil {
			return rerr
		}
		if n == 0 {
			continue
		}
		toggled := false
		switch b0 := buf[0]; {
		case b0 == 0x1b && n >= 3 && buf[1] == '[':
			switch buf[2] {
			case 'A':
				cursor--
			case 'B':
				cursor++
			case 'C', 'D': // ←/→ also toggle
				toggled = true
			}
		case b0 == 0x1b, b0 == 0x03, b0 == 'q', b0 == 'Q': // Esc / Ctrl-C / q
			return nil
		case b0 == ' ', b0 == 0x0d, b0 == 0x0a: // Space / Enter
			toggled = true
		}
		if cursor < 0 {
			cursor = 0
		}
		if cursor >= len(rows) {
			cursor = len(rows) - 1
		}
		if toggled && len(rows) > 0 {
			row := &rows[cursor]
			v := !row.get(&s)
			row.set(&s, v)
			_ = config.SaveSettings(s)
			cnt := 0
			if row.apply != nil {
				cnt = row.apply(v)
			}
			status = "\x1b[38;5;35m✓ Saved\x1b[0m  \x1b[2m" + row.label + " " + onOffWord(v) + appliedSuffix(cnt) + "\x1b[0m"
		}
		drawSettings(out, rows, &s, cursor, status)
	}
}

// drawSettings renders one full frame. Raw mode is on so lines end in \r\n.
func drawSettings(out io.Writer, rows []settingRow, s *config.Settings, cursor int, status string) {
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H\r\n")
	b.WriteString("  \x1b[1mreminal settings\x1b[0m\r\n")
	b.WriteString("  \x1b[2m↑/↓ move · Space/Enter/←→ toggle · q quit\x1b[0m\r\n\r\n")
	for i := range rows {
		sel := i == cursor
		on := rows[i].get(s)
		marker := "   "
		label := rows[i].label
		if sel {
			marker = " \x1b[38;5;39m❯\x1b[0m "
			label = "\x1b[1m" + label + "\x1b[0m"
		}
		pad := 28 - len([]rune(rows[i].label))
		if pad < 1 {
			pad = 1
		}
		b.WriteString(marker + label + strings.Repeat(" ", pad) + toggleGlyph(on) + "\r\n")
		b.WriteString("     \x1b[2m" + wrapDim(rows[i].desc, 64) + "\x1b[0m\r\n\r\n")
	}
	if status != "" {
		b.WriteString("  " + status + "\r\n")
	}
	fmt.Fprint(out, b.String())
}

// toggleGlyph renders a small on/off switch: a colored track with the knob to
// the right (on, green) or left (off, gray), plus the word.
func toggleGlyph(on bool) string {
	if on {
		return "\x1b[48;5;35m\x1b[97m  ●\x1b[0m \x1b[1;38;5;35mOn\x1b[0m"
	}
	return "\x1b[48;5;238m\x1b[38;5;252m●  \x1b[0m \x1b[2mOff\x1b[0m"
}

// wrapDim soft-wraps a description to width, indenting continuation lines to
// align under the first.
func wrapDim(text string, width int) string {
	words := strings.Fields(text)
	var b strings.Builder
	col := 0
	for i, w := range words {
		if col > 0 && col+1+len(w) > width {
			b.WriteString("\r\n     ")
			col = 0
		} else if i > 0 {
			b.WriteString(" ")
			col++
		}
		b.WriteString(w)
		col += len(w)
	}
	return b.String()
}
