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
//   - A placeholder, by agent, as captured: Qwen draws "Type your message or
//     @path/to/file" in 256-colour 103. Gemini draws the same words, but its
//     captures hold no styles yet, so for Gemini that exact text is read as
//     empty. Claude Code 2.1.328 draws none; a placeholder it might draw is
//     not pinned, so any text after its sign is a draft (refused).
//   - Anything else after the sign, or on the draft's own rows before the
//     box's closing edge, is typed text.
// A row that cannot be read is a draft: refusing costs a spoken line; typing
// into a draft sends something the person did not mean to.

const speechDraft = "draft" // the box holds something typed: never pasted after it

// voicePlaceholderText is, by agent, the placeholder text read as empty
// where its style is not pinned from a capture.
var voicePlaceholderText = map[string]string{
	"gemini": "Type your message or @path/to/file",
}

// voicePlaceholderFG is, by agent, the foreground colour its placeholder is
// drawn in, as the screen holds it ("38;5;103" for 256-colour 103).
var voicePlaceholderFG = map[string]string{
	"qwen": "38;5;103",
}

// voiceBoxDraft reports whether prog's box is on screen with something typed
// in it. False when there is no box (voiceBoxDrawn says that).
func voiceBoxDraft(prog, render string) bool {
	re := voiceBoxMarks[prog]
	if re == nil {
		return false
	}
	plain, styled := voiceBottomRowsStyled(render)
	text := strings.Join(plain, "\n")
	for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
		below := text[m[1]:]
		if !closedBelow(voiceBoxBottoms[prog], below) || (m[2] >= 0 && isChoiceRow(text[m[2]:m[3]], below)) {
			continue
		}
		prompt := strings.Count(text[:m[0]], "\n") + 1 // the row under the top edge
		if prompt >= len(styled) {
			return true // cannot be read: a draft
		}
		if typedAfterSign(prog, styled[prompt]) {
			return true
		}
		for i := prompt + 1; i < len(plain) && !voiceBoxBottoms[prog].MatchString(plain[i]); i++ {
			if typedIn(prog, styled[i]) {
				return true
			}
		}
		return false
	}
	return false
}

// voiceBottomRowsStyled is voiceBottomRows keeping each row's styled form
// beside its plain one, row for row.
func voiceBottomRowsStyled(render string) (plain, styled []string) {
	for _, row := range strings.Split(render, "\n") {
		p := stripANSI(row)
		if strings.TrimSpace(p) != "" {
			plain = append(plain, strings.TrimRight(p, " "))
			styled = append(styled, row)
		}
	}
	if len(plain) > voiceBoxRows {
		plain, styled = plain[len(plain)-voiceBoxRows:], styled[len(styled)-voiceBoxRows:]
	}
	return plain, styled
}

// typedAfterSign reports whether the prompt row holds typed text after its
// sign ("❯" or ">") and the space that follows it.
func typedAfterSign(prog, row string) bool {
	cells := styledCells(row)
	i := 0
	for i < len(cells) && (cells[i].r == ' ' || cells[i].r == ' ') {
		i++
	}
	if i < len(cells) && (cells[i].r == '❯' || cells[i].r == '>') {
		i++
	} else {
		return true // no sign where it should be: cannot be read
	}
	if i < len(cells) && (cells[i].r == ' ' || cells[i].r == ' ') && !cells[i].cursor() {
		i++
	}
	return typedCells(prog, cells[i:])
}

// typedIn reports whether a row of the box below its prompt row (a draft
// running on) holds typed text.
func typedIn(prog, row string) bool { return typedCells(prog, styledCells(row)) }

func typedCells(prog string, cells []styledCell) bool {
	var rest strings.Builder
	phFG := voicePlaceholderFG[prog]
	for _, c := range cells {
		switch {
		case c.cursor():
		case phFG != "" && c.fg == phFG:
		default:
			rest.WriteRune(c.r)
		}
	}
	text := strings.TrimSpace(strings.ReplaceAll(rest.String(), " ", " "))
	if text == "" {
		return false
	}
	if ph := voicePlaceholderText[prog]; ph != "" && text == ph {
		return false
	}
	return true
}

type styledCell struct {
	r                 rune
	reverse, underlne bool
	fg                string
}

// cursor reports whether the cell is the cursor: a space drawn in reverse
// video or underlined.
func (c styledCell) cursor() bool { return (c.r == ' ' || c.r == ' ') && (c.reverse || c.underlne) }

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
