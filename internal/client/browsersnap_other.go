// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !linux

package client

// Snap browsers are a Linux matter (browsersnap_linux.go).

import (
	"errors"
	"net"
	"os"
)

func SnapBrowserDir(home string) string                  { return "" }
func SnapBrowserHelper(home string) string               { return "" }
func SnapBrowserProfile() string                         { return "" }
func SnapBrowserSetUp(home, exe string) error            { return errors.New("no snap browsers here") }
func SnapBrowserSetUpDone(home, exe string) (bool, bool) { return false, false }
func SnapBrowserRemove(home string) error                { return nil }
func snapBrowserGone(dir *os.File) bool                  { return true }
func snapBrowserUnlisten(dir *os.File)                   {}
func browserHostSockInSnap() string                      { return "" }
func snapBrowserListen(home string) (net.Listener, *os.File, error) {
	return nil, nil, errors.New("no snap browsers here")
}

func SnapBrowserCanSetUp(home string) bool             { return false }
func SnapBrowserManifestPath(home, name string) string { return "" }
func SnapBrowserWriteManifest(home, name string, data []byte) error {
	return errors.New("no snap browsers here")
}
func SnapBrowserManifest(home, name string) ([]byte, error) {
	return nil, errors.New("no snap browsers here")
}
func SnapBrowserRemoveManifest(home, name string) error { return nil }
