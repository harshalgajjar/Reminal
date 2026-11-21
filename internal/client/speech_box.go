package client

import (
	"regexp"
	"strconv"
	"strings"
)

// voiceBoxMarks are, by program, the shape of the agent's input box at rest,
// for voice alone: where reminal can find the box, it can read whether the
// person has already typed something in it (speech_draft.go). Each is the
// box's top edge with the prompt row right under it (its text after the
// prompt sign captured): Claude Code's "❯", Qwen's and Gemini's ">". A shell
// mode ("!") or Claude's memory mode ("#") draws another sign and is not the
// box. The sign is followed by a space, a no-break space (Claude Code draws
// one) or nothing. Gemini with NO_COLOR draws its box's edges as "─" rules
// instead of its half blocks (and its sent messages with no edges at all).
// Kept apart from attnPromptMarks, which also decides the attention state.
var voiceBoxMarks = map[string]*regexp.Regexp{
	"claude": regexp.MustCompile(`(?m)^[ \t]*─{20,}\S?[ \t]*\n[ \t]*❯([ \t\x{00a0}][^\n]*)?$`),
	"qwen":   regexp.MustCompile(`(?m)^[ \t]*─{20,}\S?[ \t]*\n>([ \t\x{00a0}][^\n]*)?$`),
	"gemini": regexp.MustCompile(`(?m)^[ \t]*(?:▄{20,}|─{20,})\S?[ \t]*\n[ \t]*>([ \t\x{00a0}][^\n]*)?$`),
}

// voiceBoxRows is how many non-empty rows from the bottom the box is looked
// for in: at rest an agent's box sits right above its footer. Close to the
// bottom, because Gemini draws the messages already sent in the same box
// shape, and one of those is not the box. Blank rows are skipped (some
// agents paint every row, some leave a gap).
const voiceBoxRows = 7

var (
	numberedRow = regexp.MustCompile(`^[ \t\x{00a0}]*([0-9]+)\.[ \t\x{00a0}]`)
	// choiceFooter is a picker's own help row: whatever the cursor row reads,
	// a list of choices is on screen.
	choiceFooter = regexp.MustCompile(`(?i)enter to (select|confirm)|enter: select|↑/↓|to navigate`)
)

// voiceBottomRows is the bottom voiceBoxRows non-empty rows of a screen,
// joined: where an agent's box at rest is looked for.
func voiceBottomRows(render string) string {
	var rows []string
	for _, row := range strings.Split(stripANSI(render), "\n") {
		if strings.TrimSpace(row) != "" {
			rows = append(rows, strings.TrimRight(row, " "))
		}
	}
	if len(rows) > voiceBoxRows {
		rows = rows[len(rows)-voiceBoxRows:]
	}
	return strings.Join(rows, "\n")
}

// voiceBoxBottoms are, by program, the box's closing edge: the input box is
// bracketed, an edge above its prompt rows and one below (Claude Code and
// Qwen draw "─" for both, Gemini "▄" above and "▀" below). An option row in a
// dialog is not, whatever its text.
var voiceBoxBottoms = map[string]*regexp.Regexp{
	"claude": regexp.MustCompile(`^[ \t]*─{20,}\S?[ \t]*$`),
	"qwen":   regexp.MustCompile(`^[ \t]*─{20,}\S?[ \t]*$`),
	"gemini": regexp.MustCompile(`^[ \t]*(?:▀{20,}|─{20,})\S?[ \t]*$`),
}

// voiceBoxDraftRows is how many rows of a draft may sit between the prompt
// row and the box's closing edge (a draft runs on over several rows).
const voiceBoxDraftRows = 4

// voiceBoxDrawn reports whether prog's input box is on screen: its top edge,
// its prompt row, and its closing edge within a draft's rows, the prompt row
// not the cursor row of a list of choices.
func voiceBoxDrawn(prog, render string) bool {
	re := voiceBoxMarks[prog]
	if re == nil {
		return false
	}
	text := voiceBottomRows(render)
	for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
		below := text[m[1]:]
		if !closedBelow(voiceBoxBottoms[prog], below) {
			continue
		}
		// Claude Code's question picker has a rule between its options, so
		// an option can sit between two rules: the list is told apart too.
		if m[2] >= 0 && isChoiceRow(text[m[2]:m[3]], below) {
			continue
		}
		return true
	}
	return false
}

// closedBelow reports whether the box's closing edge follows the prompt row
// within a draft's rows.
func closedBelow(bottom *regexp.Regexp, below string) bool {
	rows := strings.Split(strings.TrimPrefix(below, "\n"), "\n")
	for i, row := range rows {
		if i > voiceBoxDraftRows {
			return false
		}
		if bottom.MatchString(row) {
			return true
		}
	}
	return false
}

// isChoiceRow says whether a prompt row's text is the cursor row of a list
// of choices ("❯ 1. Yes") rather than a draft in the box ("❯ 42", "❯ 1. buy
// milk"): it reads as an item numbered n, and a later row reads as item n+1
// (a picker may put a description row under each item), or a picker's help
// row is on screen.
func isChoiceRow(row, below string) bool {
	m := numberedRow.FindStringSubmatch(row)
	if m == nil {
		return false
	}
	if choiceFooter.MatchString(below) {
		return true
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return true
	}
	for _, r := range strings.Split(below, "\n") {
		if next := numberedRow.FindStringSubmatch(r); next != nil && next[1] == strconv.Itoa(n+1) {
			return true
		}
	}
	return false
}
