// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !darwin

package client

// The macOS backend lives in the same file as the others, so these have to
// exist everywhere even though only darwin calls them. False means "not
// injected here", which is the answer on every other platform.

func keyHelperType(string) bool { return false }

func keyHelperKey(int, []string) bool { return false }

func keyHelperClick(int, int, bool, int) bool { return false }

func keyHelperScroll(int, int, int, int, string) bool { return false }

func keyHelperDrag(string, int, int) bool { return false }
