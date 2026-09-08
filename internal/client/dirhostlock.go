// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "os"

// The directory-host lock ensures exactly ONE agent on this machine serves the
// owner directory channel at a time. Without it every session's directory host
// races for the same relay channel — and because the production relay
// *supersedes* an agent that reconnects with a matching credential (while the
// local relay *rejects* it), sibling sessions would ping-pong the channel on
// every reclaim, so the machine flaps online/offline and in-flight owner
// handshakes/spawns get dropped.
const dirHostLockName = "dirhost.lock"

// tryLockDirHost attempts to become this machine's sole directory host WITHOUT
// blocking. On success it returns the held lock file — keep it open for the
// lock's lifetime and release it with unlockDirHost.
// A lock that could not be attempted is treated the same as one held by a
// sibling: don't serve the directory channel, and try again on the next poll.
func tryLockDirHost() (*os.File, bool) {
	f, held, _ := tryLockFile(dirHostLockName)
	return f, held
}

// unlockDirHost releases the directory-host lock.
func unlockDirHost(f *os.File) { unlockFile(f) }
