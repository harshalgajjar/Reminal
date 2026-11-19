// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !linux && !windows

package pty

import "golang.org/x/sys/unix"

const getTermios = unix.TIOCGETA
