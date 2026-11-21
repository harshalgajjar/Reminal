// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package main

// Only Windows keeps a browser's native messaging hosts in a registry; here
// the manifest in the browser's own folder is all there is.
func chromeRegSet(key, manifest string) error { return nil }
func chromeRegGet(key string) string          { return "" }
func chromeRegDel(key string) error           { return nil }
