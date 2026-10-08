// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build ignore

// An idle coding agent at its prompt, built as a binary named "claude": it
// draws its prompt and footer once, repaints the whole of it on SIGWINCH (a
// viewer connecting or resizing), and later changes only its footer (an
// "update installed" notice). Nothing it does is a turn.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func draw(footer string) {
	fmt.Print("\x1b[2J\x1b[H ▐▛███▜▌ Claude Code\n\n────────\n❯ \n────────\n  ⏵⏵ bypass permissions on · " + footer + "\n")
}

func main() {
	footer := "new task? /clear to save 120k tokens"
	draw(footer)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	later := time.After(40 * time.Second)
	for {
		select {
		case <-winch:
			draw(footer)
		case <-later:
			footer = "✔ Update installed · Restart to update"
			draw(footer)
		}
	}
}
