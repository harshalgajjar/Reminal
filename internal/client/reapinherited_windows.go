//go:build windows

package client

// Windows has no zombie processes: a process object disappears once the last
// handle to it closes, so there is nothing to reap.
func reapInherited() int { return 0 }
