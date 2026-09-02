// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package client

import (
	"encoding/binary"
	"testing"
)

func TestStartupApprovedEnabledFormat(t *testing.T) {
	b := startupApprovedEnabled()
	if len(b) != 12 {
		t.Fatalf("len=%d, want 12", len(b))
	}
	if b[0] != 0x02 || b[1] != 0 || b[2] != 0 || b[3] != 0 {
		t.Fatalf("enabled prefix = %v, want 02 00 00 00", b[:4])
	}
	if binary.LittleEndian.Uint64(b[4:]) < filetimeEpochOffset {
		t.Fatalf("FILETIME %d is before 1970 — Explorer may ignore the approval", binary.LittleEndian.Uint64(b[4:]))
	}
}
