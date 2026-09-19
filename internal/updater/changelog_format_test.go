package updater

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The Host panel renders a release body as plain text with `code` spans — see
// changelog/README.md. Inline markdown is not parsed, so `**bold**` reaches the
// user as literal asterisks. That shipped twice (3.13.2, 3.13.7) before anyone
// noticed, because the mistake is invisible on GitHub, where the same text
// renders correctly. This is the check that would have caught it.
func TestChangelogsCarryNoInlineMarkdown(t *testing.T) {
	dir := filepath.Join("..", "..", "changelog")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no changelog dir: %v", err)
	}

	link := regexp.MustCompile(`\[[^\]]*\]\([^)]*\)`)
	for _, e := range entries {
		name := e.Name()
		// README.md documents these rules and is read on GitHub, not in the panel.
		if e.IsDir() || !strings.HasSuffix(name, ".md") || name == "README.md" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			where := func(what string) string {
				return name + ":" + itoa(i+1) + ": " + what + " renders literally in the Host panel — " + strings.TrimSpace(line)
			}
			if strings.Contains(line, "**") {
				t.Error(where("**bold**"))
			}
			if link.MatchString(line) {
				t.Error(where("[a](link)"))
			}
			// A lone * is italics; a bullet's leading "- " is fine, and so is a
			// literal * inside a code span, which the panel does render.
			if stripped := stripCode(line); strings.Contains(stripped, "*") {
				t.Error(where("*italics*"))
			}
		}
	}
}

// stripCode removes `code` spans, whose contents the panel renders verbatim.
func stripCode(line string) string {
	out := make([]rune, 0, len(line))
	in := false
	for _, r := range line {
		if r == '`' {
			in = !in
			continue
		}
		if !in {
			out = append(out, r)
		}
	}
	return string(out)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
