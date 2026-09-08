// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/reminal/reminal/internal/client"
)

func snap(pct int, state string, mins int, at time.Time, stale bool) *client.BatterySnapshot {
	return &client.BatterySnapshot{Pct: pct, State: state, Mins: mins, At: at, Stale: stale}
}

func TestBatteryLabel(t *testing.T) {
	now := time.Now()
	// Deterministic offsets, not wall-clock times: a fixed "3:04pm today" is in
	// the FUTURE on a runner whose clock has not reached 3pm, and whenLabel
	// (correctly) calls a future timestamp "just now". Three hours ago is
	// always in the past, and stays on the same calendar day unless the test
	// runs in the first three hours of one — which the guard below handles.
	today := now.Add(-3 * time.Hour)
	if today.Day() != now.Day() {
		today = now.Add(-2 * time.Minute) // just after midnight: still today
	}
	yest := now.AddDate(0, 0, -1)

	cases := []struct {
		name string
		in   *client.BatterySnapshot
		want []string // substrings that must all appear
		none bool     // expect the empty string
	}{
		{name: "no battery at all", in: nil, none: true},
		{
			name: "discharging with an estimate",
			in:   snap(62, "discharging", 134, now, false),
			want: []string{"62%", "2h 14m", "left"},
		},
		{
			name: "charging says to full, not left",
			in:   snap(41, "charging", 72, now, false),
			want: []string{"41%", "charging", "1h 12m", "to full"},
		},
		{
			name: "charged shows no time estimate",
			in:   snap(100, "charged", 0, now, false),
			want: []string{"100%", "charged"},
		},
		{
			name: "stale reads as past tense with a clock time",
			in:   snap(20, "discharging", 62, today, true),
			want: []string{"was 20%", "at " + today.Format("3:04pm"), "1h 2m", "then"},
		},
		{
			name: "stale from yesterday says so",
			in:   snap(20, "discharging", 0, yest, true),
			want: []string{"was 20%", "yesterday", yest.Format("3:04pm")},
		},
		{
			name: "no OS estimate omits the duration entirely",
			in:   snap(55, "discharging", 0, now, false),
			want: []string{"55%"},
		},
	}

	for _, c := range cases {
		got := batteryLabel(c.in)
		if c.none {
			if got != "" {
				t.Errorf("%s: want empty, got %q", c.name, got)
			}
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q missing %q", c.name, got, w)
			}
		}
		// A fresh reading must never read as history, and vice versa —
		// confusing the two is the one mistake that makes the feature lie.
		if c.in.Stale && !strings.Contains(got, "was ") {
			t.Errorf("%s: stale reading %q does not say it is history", c.name, got)
		}
		if !c.in.Stale && strings.Contains(got, "was ") {
			t.Errorf("%s: fresh reading %q reads as history", c.name, got)
		}
	}
}

func TestBatteryLabelNoEstimateWhenCharged(t *testing.T) {
	// A charged battery reports "0:00 remaining", which must not render as a
	// countdown to empty.
	got := batteryLabel(snap(100, "charged", 0, time.Now(), false))
	if strings.Contains(got, "left") || strings.Contains(got, "to full") {
		t.Errorf("charged battery rendered a time estimate: %q", got)
	}
}

func TestDurLabel(t *testing.T) {
	cases := map[int]string{
		0: "", -5: "", 1: "1m", 59: "59m", 60: "1h", 61: "1h 1m", 134: "2h 14m", 1440: "24h",
	}
	for in, want := range cases {
		if got := durLabel(in); got != want {
			t.Errorf("durLabel(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestBatteryLabelHasNoPictographs keeps the badge plain text. It renders in a
// terminal beside hostnames and session ids, where an emoji is both
// out of place and a width hazard (most are double-width, and the machine
// header pads columns by counting runes).
func TestBatteryLabelHasNoPictographs(t *testing.T) {
	all := []*client.BatterySnapshot{
		snap(62, "discharging", 134, time.Now(), false),
		snap(41, "charging", 72, time.Now(), false),
		snap(100, "charged", 0, time.Now(), false),
		snap(7, "discharging", 12, time.Now(), false),
		snap(20, "discharging", 62, time.Now(), true),
	}
	for _, b := range all {
		for _, r := range batteryLabel(b) {
			// Emoji, pictographs, dingbats, block elements and the
			// variation selectors that follow them.
			if (r >= 0x2190 && r <= 0x2BFF) || (r >= 0x1F000 && r <= 0x1FAFF) || r == 0xFE0F {
				t.Errorf("battery label %q contains non-text glyph %q (U+%04X)", batteryLabel(b), r, r)
			}
		}
	}
}

// TestBatteryChargingShowsTimeToFull pins the distinction that matters while a
// machine is plugged in: the OS estimate is time until CHARGED, and calling it
// "left" would read as time until dead — the opposite of the truth.
func TestBatteryChargingShowsTimeToFull(t *testing.T) {
	got := batteryLabel(snap(41, "charging", 72, time.Now(), false))
	if !strings.Contains(got, "to full") {
		t.Errorf("charging label %q does not say what the estimate counts down to", got)
	}
	if strings.Contains(got, "left") {
		t.Errorf("charging label %q says \"left\", which reads as time until dead", got)
	}
}

// TestWhenLabelAt pins the wording against a fixed clock, so it cannot depend
// on when or where the suite runs.
func TestWhenLabelAt(t *testing.T) {
	now := time.Date(2026, 9, 8, 14, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"seconds ago", now.Add(-20 * time.Second), "just now"},
		{"earlier today", now.Add(-4 * time.Hour), "at 10:30am"},
		{"yesterday", now.AddDate(0, 0, -1), "yesterday 2:30pm"},
		{"last week", now.AddDate(0, 0, -6), "on Sep 2, 2:30pm"},
		{"zero value", time.Time{}, "at an unknown time"},
		// A backwards clock correction can leave a stored reading in the
		// future; it must not render as a time that has not happened.
		{"slightly ahead", now.Add(30 * time.Second), "just now"},
	}
	for _, c := range cases {
		if got := whenLabelAt(c.at, now); got != c.want {
			t.Errorf("%s: whenLabelAt = %q, want %q", c.name, got, c.want)
		}
	}
}
