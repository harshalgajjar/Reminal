package main

import (
	"os"
	"testing"
	"time"

	"github.com/reminal/reminal/internal/session"
)

// writeRec persists a live (PID == us, so pidAlive) session record into the
// test's isolated HOME so resolveActive can read it back.
func writeRec(t *testing.T, a session.Active) {
	t.Helper()
	a.PID = os.Getpid()
	if a.StartedAt.IsZero() {
		a.StartedAt = time.Now()
	}
	if a.Kind == "" {
		a.Kind = session.KindShell
	}
	if err := session.WriteActive(a); err != nil {
		t.Fatalf("WriteActive(%s): %v", a.ID, err)
	}
}

func TestResolveActivePrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("REMINAL_SESSION", "")

	writeRec(t, session.Active{ID: "ABCD1234", PIN: "1", Name: "deploy", Cwd: "/home/u/web"})
	writeRec(t, session.Active{ID: "ABCE9999", PIN: "2", Name: "build", Cwd: "/home/u/api"})
	writeRec(t, session.Active{ID: "ZZZZ0000", PIN: "3", Cwd: "/tmp/scratch"}) // unnamed

	cases := []struct {
		name    string
		arg     string
		wantID  string
		wantErr bool
	}{
		{"exact id keeps working", "ABCD1234", "ABCD1234", false},
		{"exact id case-insensitive", "abcd1234", "ABCD1234", false},
		{"exact name", "deploy", "ABCD1234", false},
		{"exact name case-insensitive", "BUILD", "ABCE9999", false},
		{"unique id prefix", "ABCD", "ABCD1234", false},
		{"ambiguous id prefix errors", "ABC", "", true},
		{"substring of cwd", "scratch", "ZZZZ0000", false},
		{"substring of name", "epl", "ABCD1234", false},
		{"no match errors", "nonexistent", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveActive(tc.arg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got session %s", tc.arg, got.ID)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveActive(%q): %v", tc.arg, err)
			}
			if got.ID != tc.wantID {
				t.Fatalf("resolveActive(%q) = %s, want %s", tc.arg, got.ID, tc.wantID)
			}
		})
	}
}

func TestResolveActiveByPortStillWorks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("REMINAL_SESSION", "")
	writeRec(t, session.Active{ID: "PORT1111", PIN: "1", Kind: session.KindPort, Port: 8080})

	got, err := resolveActive("8080")
	if err != nil {
		t.Fatalf("resolveActive(8080): %v", err)
	}
	if got.ID != "PORT1111" {
		t.Fatalf("got %s, want PORT1111", got.ID)
	}
}

func TestParseDuration(t *testing.T) {
	ok := map[string]time.Duration{
		"30m":    30 * time.Minute,
		"12h":    12 * time.Hour,
		"1d":     24 * time.Hour,
		"2w":     14 * 24 * time.Hour,
		"1h30m":  90 * time.Minute,
		"90s":    90 * time.Second,
	}
	for in, want := range ok {
		got, err := parseDuration(in)
		if err != nil {
			t.Errorf("parseDuration(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseDuration(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"", "abc", "1y", "-3h", "d", "1.5d"} {
		if _, err := parseDuration(bad); err == nil {
			t.Errorf("parseDuration(%q) should have errored", bad)
		}
	}
}

func TestHumanShort(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second: "30s",
		5 * time.Minute:  "5m",
		3 * time.Hour:    "3h",
		50 * time.Hour:   "2d",
	}
	for d, want := range cases {
		if got := humanShort(d); got != want {
			t.Errorf("humanShort(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestAbbrevHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := abbrevHome(home); got != "~" {
		t.Errorf("abbrevHome(home) = %q, want ~", got)
	}
	if got := abbrevHome(home + "/project"); got != "~/project" {
		t.Errorf("abbrevHome(home/project) = %q, want ~/project", got)
	}
	if got := abbrevHome("/etc/hosts"); got != "/etc/hosts" {
		t.Errorf("abbrevHome(/etc/hosts) = %q, want unchanged", got)
	}
}
