// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package main

import "path/filepath"

// browserRealPath is p with every link on the way followed.
func browserRealPath(p string) (string, error) { return filepath.EvalSymlinks(p) }

// browserNetworkDrive: drive letters are a Windows matter.
func browserNetworkDrive(string) bool { return false }
