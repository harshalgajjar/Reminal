// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// What `reminal integrate` prints, fitted to the terminal it is read in. A
// phone's terminal is some 40 columns wide: a row or a sentence written for
// 80 is cut there in the middle of a word. So every line integrate prints
// goes through here: sentences wrapped on their words, and a row's text
// wrapped under its own column.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

const (
	// integrateWidthDefault is the width with no terminal to ask (piped, run
	// by the daemon) and no COLUMNS.
	integrateWidthDefault = 80
	// integrateWidthMin is the least integrate wraps to: narrower, a row has
	// no room left beside its name.
	integrateWidthMin = 40
)

// integrateWidth is how many columns integrate's lines may take: the
// terminal's own, else COLUMNS, else 80, and never under 40. A var so a test
// can set it.
var integrateWidth = func() int {
	w := integrateWidthDefault
	if tw, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && tw > 0 {
		w = tw
	} else if cw, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && cw > 0 {
		w = cw
	}
	return max(w, integrateWidthMin)
}

// noBreakSpace stands for a space a line is never broken at.
const noBreakSpace = ' '

// noBreak keeps s on one line when it is wrapped: a path or a command with
// spaces in it, which is to be read (and copied) whole.
func noBreak(s string) string {
	return strings.ReplaceAll(s, " ", string(noBreakSpace))
}

// wrapWords wraps text on its words so that no line is wider than width:
// the first line led by first, each one after by rest (a hanging indent).
// A word is never broken, so one longer than the room there is stands alone
// on a line that is too wide: the only such line. Always at least one line.
func wrapWords(text string, width int, first, rest string) []string {
	words := strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == '\n' || r == '\t' })
	var lines []string
	lead, cur, n := first, "", 0
	for _, word := range words {
		word = strings.ReplaceAll(word, string(noBreakSpace), " ")
		wn := utf8.RuneCountInString(word)
		if cur != "" && utf8.RuneCountInString(lead)+n+1+wn > width {
			lines = append(lines, lead+cur)
			lead, cur, n = rest, "", 0
		}
		if cur != "" {
			cur += " "
			n++
		}
		cur += word
		n += wn
	}
	if cur == "" && len(lines) > 0 {
		return lines
	}
	return append(lines, strings.TrimRight(lead+cur, " "))
}

// integrateOut prints integrate's lines at one width, its rows with one name
// column.
type integrateOut struct {
	width int
	nameW int
}

// newIntegrateOut is for rows with these names: the name column is as wide
// as the longest of them, so on a narrow terminal the text beside it has the
// room a fixed column would waste.
func newIntegrateOut(names ...string) integrateOut {
	o := integrateOut{width: integrateWidth()}
	for _, n := range names {
		o.nameW = max(o.nameW, utf8.RuneCountInString(n))
	}
	return o
}

// plain is for text that is not dimmed.
func plain(s string) string { return s }

// para prints a paragraph, wrapped; indent leads its first line and hang
// each line after. style colours each line's text (not its indent).
func (o integrateOut) para(indent, hang, text string, style func(string) string) {
	for i, ln := range wrapWords(text, o.width, indent, hang) {
		lead := indent
		if i > 0 {
			lead = hang
		}
		fmt.Println(lead + style(strings.TrimPrefix(ln, lead)))
	}
}

// rowTextMin is the least room a row's text needs beside its name.
const rowTextMin = 16

// row prints one row: a mark (a tick, a cross; "" in a plan, which has
// none), the name, and text wrapped under its own column.
func (o integrateOut) row(mark, name, text string, style func(string) string) {
	lead := "  "
	if mark != "" {
		lead += mark + " "
	}
	if text == "" {
		fmt.Println(strings.TrimRight(lead+name, " "))
		return
	}
	pad := strings.Repeat(" ", max(0, o.nameW-utf8.RuneCountInString(name)))
	col := utf8.RuneCountInString(lead) + max(o.nameW, utf8.RuneCountInString(name)) + 2
	// Under the name instead, where the column beside it is too narrow to
	// read, or narrower than a word of the text (a path).
	longest := 0
	for _, word := range strings.Fields(strings.ReplaceAll(text, string(noBreakSpace), "_")) {
		longest = max(longest, utf8.RuneCountInString(word))
	}
	if o.width-col < rowTextMin || longest > o.width-col {
		if name != "" {
			fmt.Println(lead + name)
		}
		under := strings.Repeat(" ", utf8.RuneCountInString(lead)+2)
		o.para(under, under, text, style)
		return
	}
	hang := strings.Repeat(" ", col)
	for i, ln := range wrapWords(text, o.width, hang, hang) {
		ln = style(strings.TrimPrefix(ln, hang))
		if i == 0 {
			fmt.Println(lead + name + pad + "  " + ln)
		} else {
			fmt.Println(hang + ln)
		}
	}
}
