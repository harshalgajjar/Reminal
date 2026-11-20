// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

// Package chromeext is reminal's Chrome extension, embedded so `reminal
// integrate chrome` can write it out for "Load unpacked" (it is not in the
// Chrome Web Store yet).
package chromeext

import "embed"

// Files is the extension: its manifest, scripts, pages and icons.
//
//go:embed manifest.json *.js *.html *.css icons
var Files embed.FS
