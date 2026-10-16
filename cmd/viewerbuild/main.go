// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

// Command viewerbuild writes the viewer page as the binary serves it —
// rendered from internal/client/web by internal/viewerbuild — into a
// directory, for a rig that serves one and for the check that the Worker's
// build (cloudflare/scripts/build-viewer.mjs) is the same. Nothing it
// writes is committed. Run from the repository root:
//
//	go run ./cmd/viewerbuild -out /tmp/viewer
package main

import (
	"flag"
	"fmt"
	"os"

	"reminal/internal/viewerbuild"
)

func main() {
	out := flag.String("out", "", "the directory to write the page into")
	web := flag.String("web", "internal/client/web", "the page's sources")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: viewerbuild -out DIR [-web internal/client/web]")
		os.Exit(2)
	}
	if err := viewerbuild.Write(*web, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
