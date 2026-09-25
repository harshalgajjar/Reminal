// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package main

import "os"

// mcpPollable: Windows has no exec, so there is nothing to swap onto and no
// need to wake from a read; the file is used as it is.
func mcpPollable(f *os.File) *os.File { return f }
