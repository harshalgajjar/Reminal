package client

import (
	"regexp"
	"strings"
)

// A box on screen is where words may go, but only an empty one. With a draft
// in it, the said line would be pasted after the person's half-written text
// and the Return would send the lot; with "/cl" or "/clear" there, it would
// run a slash command nobody confirmed. So a box holding anything typed is
// refused (speechDraft), and the person sends or clears it first.
//
// Whether a box is empty is read from the box's rows with their styles, as
// the screen holds them (Render keeps them as VT sequences): an empty box
// still draws things after its prompt sign, and only their styles tell them
// from typed text.
//   - The cursor: a space cell drawn in reverse video (Claude Code) or
//     underlined (Qwen).
//   - A placeholder, by agent, as captured (raw styled screens of each):
//     Qwen Code 0.28.0 and Gemini CLI 0.69.0 draw "Type your message or
//     @path/to/file": read as empty by its words, whatever its colour (a
//     prefix of them when a narrow window cuts it); as a second way in, by
//     the colour each was captured in: Qwen 256-colour 103, Gemini
//     256-colour 248 (its typed text is 231). Claude Code 2.1.328 and
//     2.1.326 drew none in any captured state (fresh, after a turn, after
//     /clear, after a cleared draft); a suggestion shown only to a
//     signed-in user could not be captured, so for Claude text drawn only
//     faint is read as a placeholder (typed text is never faint), and
//     anything else as typed.
//   - Qwen ends a draft's rows with a zero-width space: not typed text.
//   - Anything else after the sign, or on the draft's own rows before the
//     box's closing edge, is typed text.
// A row that cannot be read is a draft: refusing costs a spoken line; typing
// into a draft sends something the person did not mean to.

const speechDraft = "draft" // the box holds something typed: never pasted after it

// voicePlaceholder reports whether a cell is prog's placeholder, by its
// style as the screen holds it.
func voicePlaceholder(prog string, c styledCell) bool {
	switch prog {
	case "qwen":
		return c.fg == "38;5;103"
	case "gemini":
		return c.fg == "38;5;248"
	case "claude":
		return c.faint
	}
	return false
}

// voiceBoxDraft reports whether prog's box is on screen with something typed
// in it. False when there is no box (voiceBoxDrawn says that).
func voiceBoxDraft(prog, render string) bool {
	re, bottom := voiceBoxMarks[prog], voiceBoxBottoms[prog]
	if re == nil || bottom == nil {
		return false
	}
	plain, styled := voiceRowsStyled(render)
	// The box at rest is found by its closing edge, right above the footer:
	// a draft of many rows makes the box tall, and its top edge may be far
	// up, but its closing edge stays near the bottom. (Gemini draws messages
	// already sent in the same shape, higher up: never one of those.)
	for end := len(plain) - 1; end >= 0 && end >= len(plain)-1-voiceFooterRows; end-- {
		if !bottom.MatchString(plain[end]) {
			continue
		}
		// Its prompt row: under a top edge, with nothing but the draft's rows
		// between it and the closing edge.
		for p := end - 1; p >= 1 && p >= end-voiceDraftReadRows; p-- {
			pair := plain[p-1] + "\n" + plain[p]
			m := re.FindStringSubmatchIndex(pair)
			if m == nil || m[0] != 0 || m[1] != len(pair) {
				if bottom.MatchString(plain[p]) {
					break // another edge first: not this box's prompt row
				}
				continue
			}
			below := strings.Join(plain[p+1:], "\n")
			if m[2] >= 0 && isChoiceRow(pair[m[2]:m[3]], below) {
				break // a list of choices, not the box
			}
			if typedAfterSign(prog, styled[p]) {
				return true
			}
			for i := p + 1; i < end; i++ {
				if typedIn(prog, styled[i]) {
					return true
				}
			}
			return false
		}
	}
	return false
}

// voiceFooterRows is how many non-empty rows an agent may draw under its box
// (its hints and mode line; Qwen's and Gemini's completion menu for a
// slash command, up to a dozen rows): the box's closing edge is looked for
// no higher than that above the bottom. The lowest box found is the one.
const voiceFooterRows = 16

// voiceDraftReadRows is how many rows a draft may run to between the prompt
// row and the closing edge and still be read.
const voiceDraftReadRows = 16

// voiceRowsStyled is a screen's non-empty rows, each plain and styled, row
// for row.
func voiceRowsStyled(render string) (plain, styled []string) {
	for _, row := range strings.Split(render, "\n") {
		p := stripANSI(row)
		if strings.TrimSpace(p) != "" {
			plain = append(plain, strings.TrimRight(p, " "))
			styled = append(styled, row)
		}
	}
	return plain, styled
}

// typedAfterSign reports whether the prompt row holds typed text after its
// sign ("❯" or ">") and the space that follows it.
func typedAfterSign(prog, row string) bool {
	cells := styledCells(row)
	i := 0
	for i < len(cells) && (cells[i].r == ' ' || cells[i].r == '\u00a0') {
		i++
	}
	if i < len(cells) && (cells[i].r == '❯' || cells[i].r == '>') {
		i++
	} else {
		return true // no sign where it should be: cannot be read
	}
	if i < len(cells) && (cells[i].r == ' ' || cells[i].r == '\u00a0') && !cells[i].cursor() {
		i++
	}
	return typedCells(prog, cells[i:])
}

// typedIn reports whether a row of the box below its prompt row (a draft
// running on) holds typed text.
func typedIn(prog, row string) bool { return typedCells(prog, styledCells(row)) }

func typedCells(prog string, cells []styledCell) bool {
	var rest strings.Builder
	on := cursorOnPlaceholder(prog, cells)
	for i, c := range cells {
		switch {
		case i == on, c.cursor(), voicePlaceholder(prog, c):
		case c.r == ' ' || c.r == '​':
			rest.WriteRune(' ')
		default:
			rest.WriteRune(c.r)
		}
	}
	text := strings.Join(strings.Fields(rest.String()), " ")
	return text != "" && !isPlaceholderText(prog, text)
}

// cursorOnPlaceholder is the cell of a cursor drawn on a placeholder's
// first letter (that letter in reverse video or underlined, the rest of the
// placeholder after it in its own style), or -1. Only that shape: nothing
// but blanks before it, and after it placeholder cells alone, at least one
// of them a letter. A lone reverse letter, or one with typed text after it,
// is the cursor on a draft.
func cursorOnPlaceholder(prog string, cells []styledCell) int {
	blank := func(c styledCell) bool { return c.r == ' ' || c.r == ' ' || c.r == '​' }
	for i, c := range cells {
		if blank(c) && !c.reverse && !c.underlne {
			continue
		}
		if blank(c) || !(c.reverse || c.underlne) {
			return -1
		}
		letters := 0
		for _, n := range cells[i+1:] {
			switch {
			case blank(n):
			case voicePlaceholder(prog, n):
				letters++
			default:
				return -1
			}
		}
		if letters == 0 {
			return -1
		}
		return i
	}
	return -1
}

// voicePlaceholderText is, by agent, its placeholder's words, read as empty
// whatever colour they are drawn in: a true-colour terminal, another theme
// or none at all draws them otherwise than the captures (TERM in a reminal
// session is xterm-256color, but COLORTERM is passed on from wherever
// reminal was started). Nobody types those exact words as a draft, and
// sending them would be harmless.
var voicePlaceholderText = map[string]string{
	"qwen":   "Type your message or @path/to/file",
	"gemini": "Type your message or @path/to/file",
}

// voicePlaceholderCut is how much of a placeholder a narrow window must show
// for what is left of it to be read as the placeholder, cut at the box's
// edge.
const voicePlaceholderCut = 12

func isPlaceholderText(prog, text string) bool {
	ph := voicePlaceholderText[prog]
	if ph == "" {
		return false
	}
	return text == ph || (len(text) >= voicePlaceholderCut && strings.HasPrefix(ph, text))
}

type styledCell struct {
	r                 rune
	faint             bool
	reverse, underlne bool
	fg                string
}

// cursor reports whether the cell is the cursor: a space drawn in reverse
// video or underlined.
func (c styledCell) cursor() bool {
	return (c.r == ' ' || c.r == '\u00a0') && (c.reverse || c.underlne)
}

var sgrSeq = regexp.MustCompile(`^\x1b\[([0-9;:]*)m`)

// styledCells reads a row of the screen's render into its characters, each
// with the styles that matter here (reverse, underline, foreground colour).
// Other escape sequences are skipped.
func styledCells(row string) []styledCell {
	var cells []styledCell
	var cur styledCell
	for i := 0; i < len(row); {
		if row[i] == 0x1b {
			if m := sgrSeq.FindStringSubmatch(row[i:]); m != nil {
				applySGR(&cur, m[1])
				i += len(m[0])
				continue
			}
			// Another sequence: skip to its final byte.
			j := i + 1
			if j < len(row) && row[j] == '[' {
				j++
				for j < len(row) && (row[j] < 0x40 || row[j] > 0x7e) {
					j++
				}
			}
			i = j + 1
			continue
		}
		r := []rune(row[i:])[0]
		c := cur
		c.r = r
		cells = append(cells, c)
		i += len(string(r))
	}
	return cells
}

func applySGR(c *styledCell, params string) {
	if params == "" {
		*c = styledCell{}
		return
	}
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		switch p := ps[i]; {
		case p == "0":
			*c = styledCell{}
		case p == "2":
			c.faint = true
		case p == "22":
			c.faint = false
		case p == "7":
			c.reverse = true
		case p == "27":
			c.reverse = false
		case p == "4" || strings.HasPrefix(p, "4:"):
			c.underlne = p != "4:0"
		case p == "24":
			c.underlne = false
		case p == "39":
			c.fg = ""
		case strings.HasPrefix(p, "38:"):
			// The colon form: 38:5:N, or 38:2:R:G:B (with an empty colour
			// space, 38:2::R:G:B).
			sub := strings.Split(p, ":")
			switch {
			case len(sub) == 3 && sub[1] == "5":
				c.fg = "38;5;" + sub[2]
			case len(sub) >= 5 && sub[1] == "2":
				rgb := sub[len(sub)-3:]
				c.fg = "38;2;" + rgb[0] + ";" + rgb[1] + ";" + rgb[2]
			}
		case p == "38" && i+2 < len(ps) && ps[i+1] == "5":
			c.fg = "38;5;" + ps[i+2]
			i += 2
		case p == "38" && i+4 < len(ps) && ps[i+1] == "2":
			c.fg = "38;2;" + ps[i+2] + ";" + ps[i+3] + ";" + ps[i+4]
			i += 4
		case len(p) == 2 && (p[0] == '3' || p[0] == '9') && p[1] >= '0' && p[1] <= '7':
			c.fg = p
		}
	}
}

// draftNow reports whether prog's box is on screen now with something typed
// in it.
func (a *Agent) draftNow(prog string) bool {
	a.screenMu.Lock()
	scr := a.screen
	var render string
	if scr != nil {
		render = scr.Render()
	}
	a.screenMu.Unlock()
	return scr != nil && voiceBoxDraft(prog, render)
}
