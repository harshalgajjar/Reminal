// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin

package client

// vdisplayLoop is macOS-only: closed-lid mode's virtual display rides the
// ScreenCaptureKit helper, which doesn't exist elsewhere.
func vdisplayLoop(stop <-chan struct{}, isDaemon bool) {}
