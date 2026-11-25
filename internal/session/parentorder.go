// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package session

import "time"

// parentIsOlder: a process said to be another's parent is, only if both
// start times are known and the parent's is not after the child's. (Where
// an id can outlive its process and be given to a new one, the id a child
// holds for its parent may by now be a stranger's.)
func parentIsOlder(parent time.Time, parentKnown bool, child time.Time, childKnown bool) bool {
	return parentKnown && childKnown && !parent.After(child)
}

// parentCameFirst says whether a process said to be another's parent was
// made before it. Where the system numbers its processes in the order it
// makes them (pn, cn), that order is taken: it does not depend on the clock.
// Start times are read off the clock, and a clock set back between the two
// (a machine resumed, a time correction) made a real parent look younger
// than its child, so everything running in a session was taken to be in
// none. Times are the answer only where there are no numbers.
func parentCameFirst(pn uint64, pnKnown bool, cn uint64, cnKnown bool, parent time.Time, parentKnown bool, child time.Time, childKnown bool) bool {
	if pnKnown && cnKnown {
		return pn < cn
	}
	return parentIsOlder(parent, parentKnown, child, childKnown)
}
