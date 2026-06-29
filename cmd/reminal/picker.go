package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/reminal/reminal/internal/session"
	"golang.org/x/term"
)

// errPickCancelled is returned by pickSession when the user backs out (Esc or
// Ctrl-C) instead of choosing a session. main.go treats it as a clean,
// non-error exit so cancelling the picker doesn't print "error: …".
var errPickCancelled = errors.New("selection cancelled")

// pickViewport caps how many rows are drawn at once; the list windows around
// the cursor so arrow-keying past the edge scrolls rather than overflowing.
const pickViewport = 15

// pickSession runs a raw-mode, type-to-filter selector over the given sessions
// and returns the chosen one. It's the no-argument `reminal attach` path: type
// to narrow by id/name/cwd/title, ↑/↓ to move, Enter to attach, Esc to cancel.
// The caller must have already verified stdin is a TTY.
func pickSession(sessions []*session.Active) (*session.Active, error) {
	if len(sessions) == 0 {
		return nil, errors.New("no sessions to choose from")
	}

	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("interactive picker needs a terminal: %w", err)
	}
	defer term.Restore(fd, old)

	out := os.Stdout
	// Alt screen + hidden cursor for a clean, scrollback-preserving UI.
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")

	// Recent-first, matching `reminal list`.
	sorted := make([]*session.Active, len(sessions))
	copy(sorted, sessions)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].LastActive().After(sorted[j].LastActive())
	})

	query := ""
	cursor := 0

	filter := func() []*session.Active {
		if query == "" {
			return sorted
		}
		lower := strings.ToLower(query)
		var m []*session.Active
		for _, a := range sorted {
			if strings.Contains(strings.ToLower(a.ID), lower) ||
				strings.Contains(strings.ToLower(a.Name), lower) ||
				strings.Contains(strings.ToLower(a.Cwd), lower) ||
				strings.Contains(strings.ToLower(a.Title), lower) {
				m = append(m, a)
			}
		}
		return m
	}

	clamp := func(list []*session.Active) {
		if len(list) == 0 {
			cursor = 0
			return
		}
		if cursor < 0 {
			cursor = 0
		}
		if cursor >= len(list) {
			cursor = len(list) - 1
		}
	}

	draw(out, filter(), query, cursor)
	buf := make([]byte, 16)
	for {
		n, rerr := os.Stdin.Read(buf)
		if rerr != nil {
			return nil, rerr
		}
		if n == 0 {
			continue
		}
		b0 := buf[0]
		switch {
		case b0 == 0x1b && n >= 3 && buf[1] == '[':
			switch buf[2] {
			case 'A': // up
				cursor--
			case 'B': // down
				cursor++
			}
		case b0 == 0x1b: // lone Esc
			return nil, errPickCancelled
		case b0 == 0x03: // Ctrl-C
			return nil, errPickCancelled
		case b0 == 0x0d || b0 == 0x0a: // Enter
			list := filter()
			clamp(list)
			if len(list) == 0 {
				continue
			}
			return list[cursor], nil
		case b0 == 0x7f || b0 == 0x08: // Backspace / Ctrl-H
			if r := []rune(query); len(r) > 0 {
				query = string(r[:len(r)-1])
				cursor = 0
			}
		default:
			// Append printable bytes from this read to the filter.
			for _, c := range buf[:n] {
				if c >= 0x20 && c != 0x7f {
					query += string(rune(c))
				}
			}
			cursor = 0
		}
		list := filter()
		clamp(list)
		draw(out, list, query, cursor)
	}
}

// draw renders one full frame of the picker: header, the windowed session
// rows with the cursor highlighted, and the live filter box. Raw mode is on,
// so every line ends with CRLF.
func draw(out *os.File, list []*session.Active, query string, cursor int) {
	now := time.Now()
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H") // clear + home
	b.WriteString("  \x1b[1mAttach to a session\x1b[0m  \x1b[2m— type to filter · ↑/↓ move · Enter attach · Esc cancel\x1b[0m\r\n\r\n")

	if len(list) == 0 {
		b.WriteString("  \x1b[2m(no matches)\x1b[0m\r\n")
	}

	// Window the list around the cursor so a long list scrolls in place.
	start := 0
	if len(list) > pickViewport {
		start = cursor - pickViewport/2
		if start < 0 {
			start = 0
		}
		if start > len(list)-pickViewport {
			start = len(list) - pickViewport
		}
	}
	end := start + pickViewport
	if end > len(list) {
		end = len(list)
	}

	for i := start; i < end; i++ {
		a := list[i]
		selected := i == cursor
		b.WriteString(pickerRow(a, now, selected))
		b.WriteString("\r\n")
	}
	if start > 0 {
		b.WriteString("  \x1b[2m↑ more above\x1b[0m\r\n")
	}
	if end < len(list) {
		b.WriteString("  \x1b[2m↓ more below\x1b[0m\r\n")
	}

	b.WriteString("\r\n  \x1b[1mfilter:\x1b[0m " + query + "\x1b[7m \x1b[0m")
	fmt.Fprint(out, b.String())
}

// pickerRow formats a single session line for the picker. The selected row is
// reverse-video so it stands out regardless of theme.
func pickerRow(a *session.Active, now time.Time, selected bool) string {
	name := a.Name
	if name == "" {
		name = "—"
	}

	mode := "foreground"
	if a.Headless {
		mode = "headless"
	}

	var state string
	if a.Viewers > 0 {
		noun := "viewer"
		if a.Viewers != 1 {
			noun = "viewers"
		}
		state = fmt.Sprintf("%d %s", a.Viewers, noun)
	} else {
		state = "idle " + humanShort(a.IdleFor(now))
	}

	tail := abbrevHome(a.Cwd)
	if a.Title != "" {
		t := a.Title
		if len(t) > 40 {
			t = t[:39] + "…"
		}
		if tail != "" {
			tail += " · "
		}
		tail += t
	}

	line := fmt.Sprintf("  %-20s  %-8s  %-10s  %-12s  %s", name, a.ID, mode, state, tail)
	if selected {
		return "\x1b[7m" + line + "\x1b[0m"
	}
	return line
}
