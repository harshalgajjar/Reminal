// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package keepawake

// execStateStart is the Windows in-process inhibitor (SetThreadExecutionState);
// on every other platform the child-process paths in keepawake.go apply.
func execStateStart(display bool) (stop func(), ok bool) { return nil, false }
