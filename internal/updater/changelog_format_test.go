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
	dirs := changelogDirs(t, filepath.Join("..", ".."))
	if len(dirs) == 0 {
		t.Skip("no changelog dir")
	}
	for _, dir := range dirs {
		for _, p := range inlineMarkdown(t, dir) {
			t.Error(p)
		}
	}
}

// Every changelog directory at the root is checked, whatever follows the
// word: a tree with another (changelog-beta/, say) beside changelog/ has both
// read, with no code naming either. Here a **bold** line in changelog-beta/
// is caught, while its README.md and a directory not named changelog* are
// left alone.
func TestChangelogCheckReadsEveryChangelogDir(t *testing.T) {
	root := t.TempDir()
	for path, body := range map[string]string{
		"changelog/1.0.0.md":       "# 1.0.0\n\nFixed\n- A plain line.\n",
		"changelog-beta/2.0.0.md":  "# 2.0.0\n\nImproved\n- A **bold** claim.\n",
		"changelog-beta/README.md": "Rules: no **bold** in the notes.\n",
		"notes/0.1.0.md":           "- Not a changelog, **bold** or not.\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, dir := range changelogDirs(t, root) {
		got = append(got, inlineMarkdown(t, dir)...)
	}
	// The bold line, and only it (its asterisks are counted as italics too).
	bold := false
	for _, p := range got {
		if !strings.HasPrefix(p, "changelog-beta/2.0.0.md:4: ") {
			t.Errorf("a problem outside the bold line: %q", p)
		}
		bold = bold || strings.HasPrefix(p, "changelog-beta/2.0.0.md:4: **bold**")
	}
	if !bold {
		t.Fatalf("the bold line in changelog-beta/2.0.0.md was not caught; got %q", got)
	}
}

// changelogDirs lists the directories at root whose name starts "changelog".
func changelogDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "changelog") {
			dirs = append(dirs, filepath.Join(root, e.Name()))
		}
	}
	return dirs
}

// inlineMarkdown is what in dir's notes would render literally in the Host
// panel, one line each, named <dir>/<file>:<line>.
func inlineMarkdown(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	link := regexp.MustCompile(`\[[^\]]*\]\([^)]*\)`)
	var out []string
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
				return filepath.Base(dir) + "/" + name + ":" + itoa(i+1) + ": " + what + " renders literally in the Host panel — " + strings.TrimSpace(line)
			}
			if strings.Contains(line, "**") {
				out = append(out, where("**bold**"))
			}
			if link.MatchString(line) {
				out = append(out, where("[a](link)"))
			}
			// A lone * is italics; a bullet's leading "- " is fine, and so is a
			// literal * inside a code span, which the panel does render.
			if stripped := stripCode(line); strings.Contains(stripped, "*") {
				out = append(out, where("*italics*"))
			}
		}
	}
	return out
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
