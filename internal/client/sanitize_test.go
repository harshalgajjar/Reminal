package client

import "testing"

func TestStripControlChars(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"normal.txt", "normal.txt"},
		{"with space.png", "with space.png"},          // 0x20 is preserved
		{"emoji-🎉.txt", "emoji-🎉.txt"},                  // UTF-8 high bytes preserved
		{"\x1b[2Jboom", "[2Jboom"},                       // ESC stripped
		{"a\x00b", "ab"},                                  // NUL stripped
		{"\x07bell.txt", "bell.txt"},                     // BEL stripped
		{"x\x7fy", "xy"},                                  // DEL stripped
		{"line\nfeed", "linefeed"},                        // LF stripped
		{"\x1b]0;title\x07evil.txt", "]0;titleevil.txt"}, // OSC stripped (ESC + BEL gone)
		{"", ""},
		{"\x00\x01\x02", ""},
	}
	for _, c := range cases {
		got := stripControlChars(c.in)
		if got != c.want {
			t.Errorf("stripControlChars(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
