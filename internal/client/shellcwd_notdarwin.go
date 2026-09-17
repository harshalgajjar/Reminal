// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin

package client

// procCwdDarwin is darwin-only; elsewhere shellCwd never reaches it.
func procCwdDarwin(int) string { return "" }
