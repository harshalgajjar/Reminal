// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"testing"
	"time"
)

func TestSizeBookIgnoresGarbage(t *testing.T) {
	var b viewerSizeBook
	if b.report("phone", 80, 5) {
		t.Fatal("5-row wrap was accepted")
	}
	if !b.settledSize().zero() {
		t.Fatal("garbage report settled a size")
	}
}

func TestSizeBookFirstReportAppliesImmediately(t *testing.T) {
	var b viewerSizeBook
	if !b.report("phone", 80, 24) {
		t.Fatal("first valid report should apply immediately")
	}
	got := b.settledSize()
	if got.cols != 80 || got.rows != 24 {
		t.Fatalf("settled %dx%d, want 80x24", got.cols, got.rows)
	}
}

func TestSizeBookMinAcrossViewers(t *testing.T) {
	var b viewerSizeBook
	b.report("laptop", 100, 40)
	b.report("phone", 80, 20)
	b.mu.Lock()
	got := minTermSizes(b.wraps)
	b.mu.Unlock()
	if got.cols != 80 || got.rows != 20 {
		t.Fatalf("min = %dx%d, want 80x20", got.cols, got.rows)
	}
}

func TestSizeBookKeyboardOpenShrinksToFinalHeight(t *testing.T) {
	var b viewerSizeBook
	b.seed(map[string]termSize{"phone": {80, 40}}, termSize{80, 40})
	applied := 0
	for _, rows := range []uint16{32, 24, 20} {
		b.report("phone", 80, rows)
		b.schedule(func() {
			applied++
			s := b.settledSize()
			b.setApplied(s.cols, s.rows)
		})
	}
	time.Sleep(resizeSettle + 150*time.Millisecond)
	got := b.settledSize()
	if got.cols != 80 || got.rows != 20 {
		t.Fatalf("keyboard open settled at %dx%d, want 80x20", got.cols, got.rows)
	}
	if applied != 1 {
		t.Fatalf("keyboard animation SIGWINCHed %d times, want 1", applied)
	}
}

func TestSizeBookKeyboardCloseSettlesAtFinalHeight(t *testing.T) {
	var b viewerSizeBook
	b.seed(map[string]termSize{"phone": {80, 20}}, termSize{80, 20})
	for _, rows := range []uint16{22, 28, 40} {
		b.report("phone", 80, rows)
		b.schedule(func() {
			s := b.settledSize()
			b.setApplied(s.cols, s.rows)
		})
	}
	time.Sleep(resizeSettle + 150*time.Millisecond)
	got := b.settledSize()
	if got.cols != 80 || got.rows != 40 {
		t.Fatalf("settled %dx%d, want 80x40", got.cols, got.rows)
	}
}

func TestSizeBookAddressBarJitterWaits(t *testing.T) {
	var b viewerSizeBook
	b.seed(map[string]termSize{"phone": {80, 40}}, termSize{80, 40})
	b.report("phone", 80, 42)
	b.schedule(func() {
		s := b.settledSize()
		b.setApplied(s.cols, s.rows)
	})
	time.Sleep(resizeSettle + 150*time.Millisecond)
	if got := b.settledSize(); got.rows != 40 {
		t.Fatalf("jitter applied after settle (%d rows)", got.rows)
	}
	time.Sleep(resizeGrowStable)
	if got := b.settledSize(); got.rows != 42 {
		t.Fatalf("jitter never applied (rows=%d)", got.rows)
	}
}

func TestSizeBookLaptopCannotGrowPastPhone(t *testing.T) {
	var b viewerSizeBook
	b.seed(map[string]termSize{
		"laptop": {80, 40},
		"phone":  {80, 15},
	}, termSize{80, 15})
	b.report("laptop", 80, 40)
	b.schedule(func() {
		s := b.settledSize()
		b.setApplied(s.cols, s.rows)
	})
	time.Sleep(resizeSettle + 150*time.Millisecond)
	got := b.settledSize()
	if got.rows != 15 {
		t.Fatalf("laptop re-report grew PTY to %d rows", got.rows)
	}
}

func TestSizeBookConcurrentReportsDoNotPanic(t *testing.T) {
	var b viewerSizeBook
	b.seed(map[string]termSize{"a": {80, 40}}, termSize{80, 40})
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(id int) {
			for n := 0; n < 40; n++ {
				name := "v"
				if id%2 == 0 {
					name = "phone"
				}
				rows := uint16(20 + n%20)
				b.report(name, 80, rows)
				b.schedule(func() {
					s := b.settledSize()
					b.setApplied(s.cols, s.rows)
				})
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	got := b.lastApplied()
	if got.cols != 80 || got.rows < 8 {
		t.Fatalf("after concurrent reports applied %dx%d", got.cols, got.rows)
	}
}

func TestSizeBookForgetWrapsAllowsRegrow(t *testing.T) {
	var b viewerSizeBook
	b.seed(map[string]termSize{
		"laptop": {80, 40},
		"phone":  {80, 15},
	}, termSize{80, 15})
	b.forgetWraps()
	if !b.settledSize().zero() {
		t.Fatal("forgetWraps left a settled min")
	}
	if got := b.lastApplied(); got.rows != 15 {
		t.Fatalf("forgetWraps dropped applied size (%d)", got.rows)
	}
	if !b.report("laptop", 80, 40) {
		t.Fatal("re-report after forget should be first")
	}
	got := b.settledSize()
	if got.rows != 40 {
		t.Fatalf("remaining viewer settled at %d, want 40", got.rows)
	}
}
