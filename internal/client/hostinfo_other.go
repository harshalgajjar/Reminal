// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin && !linux && !windows

package client

// fillHostInfo is a no-op on platforms without a stats reader; the common
// fields (hostname, OS, arch, CPU count) are still populated by gatherHostInfo.
func fillHostInfo(h *HostInfo) {}
