// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin

package client

// helperRefreshes: the capture helper, and its idle refresh, are macOS's.
func helperRefreshes() bool { return false }
