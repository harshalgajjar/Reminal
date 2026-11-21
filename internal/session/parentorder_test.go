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
