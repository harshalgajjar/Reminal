package client

import "testing"

func TestFeedTitle(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"osc0 bel", []string{"\x1b]0;hello world\x07"}, "hello world"},
		{"osc2 st", []string{"\x1b]2;vim main.go\x1b\\"}, "vim main.go"},
		{"split across reads", []string{"\x1b]0;par", "tial\x07"}, "partial"},
		{"surrounded by output", []string{"out\x1b]2;mid\x07put"}, "mid"},
		{"non-title osc ignored", []string{"\x1b]12;#ff0000\x07"}, ""},
		{"latest wins", []string{"\x1b]2;first\x07\x1b]2;second\x07"}, "second"},
		{"plain output no title", []string{"just some shell output\n"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Agent{}
			for _, c := range tc.chunks {
				a.feedTitle([]byte(c))
			}
			if a.title != tc.want {
				t.Fatalf("title = %q, want %q", a.title, tc.want)
			}
		})
	}
}

func TestSanitizeTitle(t *testing.T) {
	cases := map[string]string{
		"  spaced  ":      "spaced",
		"tab\there":       "tabhere",
		"bell\x07inside":  "bellinside",
		"":                "",
		"plain":           "plain",
	}
	for in, want := range cases {
		if got := sanitizeTitle(in); got != want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
	// Over-long titles are capped.
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	if got := sanitizeTitle(string(long)); len(got) > 120 {
		t.Errorf("sanitizeTitle did not cap length: got %d", len(got))
	}
}
