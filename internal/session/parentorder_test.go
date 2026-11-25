// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import (
	"testing"
	"time"
)

func TestAParentIsOnlyOneCreatedBeforeItsChild(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	if !parentIsOlder(t0, true, t0.Add(time.Second), true) {
		t.Fatal("control: a parent created before its child is not taken")
	}
	if !parentIsOlder(t0, true, t0, true) {
		t.Fatal("a parent created in the same instant is not taken")
	}
	if parentIsOlder(t0.Add(time.Second), true, t0, true) {
		t.Fatal("a process created after the child (its id given out again) is taken as its parent")
	}
	if parentIsOlder(t0, false, t0.Add(time.Second), true) || parentIsOlder(t0, true, t0.Add(time.Second), false) {
		t.Fatal("a parent is taken when a start time is not known")
	}
}

// A clock set back between a parent's start and its child's makes the
// parent's start time the later one. Where the system numbers its processes
// in the order it makes them, that order is the answer and the parent is
// still the parent; a later process given the parent's id has a larger
// number than the child and is not. Without numbers, the times decide.
func TestAParentIsToldByTheOrderProcessesWereMadeIn(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	back := t0.Add(-9 * time.Minute) // the child's start, read after the clock went back
	if !parentCameFirst(100, true, 250, true, t0, true, back, true) {
		t.Error("a parent made first was refused because the clock went back before its child started")
	}
	if parentCameFirst(900, true, 250, true, back, true, t0, true) {
		t.Error("a later process given the parent's id was taken for the parent")
	}
	if parentCameFirst(250, true, 250, true, t0, true, t0, true) {
		t.Error("a process was taken for its own parent")
	}
	// No numbers for one of them: as before, by the times.
	if !parentCameFirst(0, false, 250, true, t0, true, t0.Add(time.Second), true) || parentCameFirst(100, true, 0, false, t0.Add(time.Second), true, t0, true) {
		t.Error("without both numbers the start times did not decide")
	}
	if parentCameFirst(0, false, 0, false, t0, false, t0, true) {
		t.Error("a parent that cannot be told at all was taken")
	}
}
