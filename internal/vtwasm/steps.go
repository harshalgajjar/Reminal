// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package vtwasm

import (
	"strconv"
	"strings"
)

// stepsToSpaces is libghostty's VT output with its cells that hold no text
// written as spaces. It steps over them (CUF, "ESC[nC"), and erases a run
// that has only a background then steps over it ("ESC[nX ESC[nC"), so as not
// to make them text in the pen before them. But a cursor step does not wrap:
// painted into a narrower terminal, what follows a step near the right edge
// piles up in the last column, and a reader of the words loses the spaces
// between them. A background run becomes spaces in its background (the pen
// is already it); a bare step becomes spaces with no pen, and the pen is
// set again after them.
func stepsToSpaces(s string) string {
	if !strings.Contains(s, "C") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	var pen sgrPen // the style in force
	for i := 0; i < len(s); {
		if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
			if s[i] == '\n' {
				pen = sgrPen{}
			}
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i + 2
		for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == ';' || s[j] == ':') {
			j++
		}
		if j >= len(s) {
			b.WriteString(s[i:])
			break
		}
		params, final := s[i+2:j], s[j]
		seq := s[i : j+1]
		switch final {
		case 'm':
			pen.apply(params)
			b.WriteString(seq)
		case 'X':
			// An erase of n with the background, then a step over it: n
			// spaces in that background.
			n := count(params)
			next := "\x1b[" + params + "C"
			if strings.HasPrefix(s[j+1:], next) {
				b.WriteString(strings.Repeat(" ", n))
				j += len(next)
			} else {
				b.WriteString(seq)
			}
		case 'C':
			n := count(params)
			if pen.plain() {
				b.WriteString(strings.Repeat(" ", n))
			} else {
				b.WriteString("\x1b[0m")
				b.WriteString(strings.Repeat(" ", n))
				b.WriteString(pen.String())
			}
		default:
			b.WriteString(seq)
		}
		i = j + 1
	}
	return b.String()
}

// count is a CSI parameter as a count: 1 when empty or not a number.
func count(params string) int {
	n, err := strconv.Atoi(params)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// sgrPen is the style in force, as SGR sets it: one value per attribute, the
// last one set, so what is written back is one sequence however many came
// (kept as every sequence since a reset, a long run of them made each step
// over empty cells write them all again).
type sgrPen struct {
	fg, bg, ul string            // a colour's parameters ("31", "38;2;1;2;3"), or "" for the default
	flags      map[string]string // per attribute group: the parameter that set it
}

// sgrGroups are the attributes a parameter sets, and the parameters that turn
// each off.
var sgrGroups = map[string]string{
	"1": "weight", "2": "weight", "22": "weight",
	"3": "italic", "23": "italic",
	"4": "underline", "21": "underline", "24": "underline",
	"5": "blink", "6": "blink", "25": "blink",
	"7": "inverse", "27": "inverse",
	"8": "hidden", "28": "hidden",
	"9": "strike", "29": "strike",
	"53": "overline", "55": "overline",
}

var sgrOff = map[string]bool{"22": true, "23": true, "24": true, "25": true, "27": true, "28": true, "29": true, "55": true}

func (p *sgrPen) apply(params string) {
	if params == "" {
		*p = sgrPen{}
		return
	}
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		v := ps[i]
		// A colour by index or by value takes the parameters after it.
		ext := func() string {
			if i+1 < len(ps) && ps[i+1] == "5" && i+2 < len(ps) {
				s := strings.Join(ps[i:i+3], ";")
				i += 2
				return s
			}
			if i+1 < len(ps) && ps[i+1] == "2" && i+4 < len(ps) {
				s := strings.Join(ps[i:i+5], ";")
				i += 4
				return s
			}
			return v
		}
		base, _, _ := strings.Cut(v, ":") // "4:3" is an underline style
		switch {
		case v == "0":
			*p = sgrPen{}
		case base == "38":
			p.fg = ext()
		case base == "48":
			p.bg = ext()
		case base == "58":
			p.ul = ext()
		case v == "39":
			p.fg = ""
		case v == "49":
			p.bg = ""
		case v == "59":
			p.ul = ""
		case len(v) == 2 && (v[0] == '3' || v[0] == '9') && v[1] >= '0' && v[1] <= '7':
			p.fg = v
		case (len(v) == 2 && v[0] == '4' && v[1] >= '0' && v[1] <= '7') || (len(v) == 3 && v[:2] == "10" && v[2] >= '0' && v[2] <= '7'):
			p.bg = v
		default:
			g, ok := sgrGroups[base]
			if !ok {
				continue
			}
			if p.flags == nil {
				p.flags = map[string]string{}
			}
			if sgrOff[base] {
				delete(p.flags, g)
			} else {
				p.flags[g] = v
			}
		}
	}
}

func (p *sgrPen) plain() bool { return p.fg == "" && p.bg == "" && p.ul == "" && len(p.flags) == 0 }

// String is the pen as one SGR sequence (after a reset).
func (p *sgrPen) String() string {
	if p.plain() {
		return ""
	}
	var ps []string
	for _, g := range []string{"weight", "italic", "underline", "blink", "inverse", "hidden", "strike", "overline"} {
		if v, ok := p.flags[g]; ok {
			ps = append(ps, v)
		}
	}
	for _, c := range []string{p.fg, p.bg, p.ul} {
		if c != "" {
			ps = append(ps, c)
		}
	}
	return "\x1b[" + strings.Join(ps, ";") + "m"
}
