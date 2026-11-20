// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

// The browser_* tools: an agent in a reminal session uses the person's own
// Chrome, through reminal's extension, once the person allows it in the
// browser for that session. The call goes to this session's process (control
// verb "browser"), which asks the daemon's browser hub as itself, which asks
// the extension (internal/client/browserd.go). Nothing leaves this machine.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"reminal/internal/client"
	"reminal/internal/session"
)

// browserControlWait covers the hub's own wait (the person's answer to the
// consent window, then the action) and the hops around it.
const browserControlWait = client.BrowserWait + 5*time.Second

func browserTools(obj func(map[string]any, ...string) map[string]any, str func(string) map[string]any) []map[string]any {
	num := func(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
	tab := num("Optional: the tab, from browser_tabs_context. Default: the session's last used tab.")
	ref := str("Optional: an element's ref from browser_read_page (e.g. \"ref_12\"). Or give x and y.")
	common := "Works only inside a reminal session on this computer, with reminal's Chrome extension (`reminal integrate chrome`). " +
		"The first call asks the person in their browser to allow this session; they may say no. " +
		"Only tabs in this session's own tab group are used. "
	return []map[string]any{
		{
			"name": "browser_tabs_context",
			"description": "List the tabs in this session's tab group in the person's browser (id, title, url, active). " + common +
				"Call it first; open a tab with browser_tabs_create if there is none.",
			"inputSchema": obj(map[string]any{}),
		},
		{
			"name":        "browser_tabs_create",
			"description": "Open a new tab in this session's tab group (made if needed, named after the session). " + common,
			"inputSchema": obj(map[string]any{"url": str("Optional URL to open (default: a blank tab).")}),
		},
		{
			"name":        "browser_navigate",
			"description": "Go to a URL in a tab of this session's group, or \"back\" / \"forward\". " + common,
			"inputSchema": obj(map[string]any{"url": str("The URL, or \"back\" or \"forward\"."), "tabId": tab}, "url"),
		},
		{
			"name":        "browser_screenshot",
			"description": "A screenshot of the tab, as an image (the visible part). Coordinates for browser_click are in its pixels. " + common,
			"inputSchema": obj(map[string]any{"tabId": tab}),
		},
		{
			"name": "browser_read_page",
			"description": "The page's accessibility tree: roles, names and values, each element with a ref (\"ref_12\") the click, type and scroll tools take. " + common +
				"filter \"interactive\" keeps only what can be clicked or typed into.",
			"inputSchema": obj(map[string]any{"tabId": tab, "filter": str("\"interactive\" or \"all\" (default all).")}),
		},
		{
			"name":        "browser_get_page_text",
			"description": "The page's readable text (its main content when it has one). " + common,
			"inputSchema": obj(map[string]any{"tabId": tab}),
		},
		{
			"name": "browser_click",
			"description": "Click an element (by ref) or a point (x, y in screenshot pixels), as a real mouse does. " + common +
				"An action in a form with a password field, or with a CAPTCHA showing, is paused: ask the person to do that part.",
			"inputSchema": obj(map[string]any{"ref": ref, "x": num("Optional x."), "y": num("Optional y."), "tabId": tab,
				"button": str("\"left\" (default), \"right\" or \"middle\"."), "double": map[string]any{"type": "boolean", "description": "Double click."}}),
		},
		{
			"name": "browser_type",
			"description": "Type text into the focused element, or into the element ref names (clicked first). " + common +
				"Never into a password or payment field: the person types those.",
			"inputSchema": obj(map[string]any{"text": str("What to type."), "ref": ref, "tabId": tab}, "text"),
		},
		{
			"name":        "browser_key",
			"description": "Press a key or a combination: \"Enter\", \"Tab\", \"Escape\", \"ArrowDown\", \"ctrl+a\", \"cmd+l\". " + common,
			"inputSchema": obj(map[string]any{"key": str("The key or combination."), "tabId": tab}, "key"),
		},
		{
			"name":        "browser_scroll",
			"description": "Scroll the page, or the element ref names, by a number of wheel steps. " + common,
			"inputSchema": obj(map[string]any{"direction": str("\"down\" (default), \"up\", \"left\" or \"right\"."), "amount": num("Wheel steps (default 3)."), "ref": ref, "tabId": tab}),
		},
	}
}

// mcpBrowser runs one browser tool through this session and turns the
// extension's answer into content blocks.
func mcpBrowser(tool string, args map[string]any) ([]any, error) {
	id := enclosingSessionID()
	if id == "" {
		return nil, errors.New("the browser tools work only inside a reminal session on this computer")
	}
	rec, err := session.ReadActiveByID(id)
	if err != nil || rec.PID <= 0 {
		return nil, fmt.Errorf("this reminal session (%s) is not running", id)
	}
	if args == nil {
		args = map[string]any{}
	}
	req, err := json.Marshal(map[string]any{"tool": strings.TrimPrefix(tool, "browser_"), "args": args})
	if err != nil {
		return nil, err
	}
	reply, err := sendControl(rec.PID, "browser "+base64.StdEncoding.EncodeToString(req), browserControlWait)
	if err != nil {
		if strings.Contains(err.Error(), "unknown") {
			return nil, errors.New("this session's reminal has no browser tools yet (it was started before an update)")
		}
		return nil, err
	}
	return browserBlocks(reply)
}

// browserBlocks reads the session's answer (base64 of client.BrowserResp):
// a screenshot as an image block, anything else as text.
func browserBlocks(reply string) ([]any, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(reply))
	if err != nil {
		return nil, err
	}
	var resp client.BrowserResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "the browser did not do it"
		}
		return nil, errors.New(resp.Error)
	}
	var shot struct {
		Image    string `json:"image"`
		MimeType string `json:"mimeType"`
		Note     string `json:"note"`
	}
	if json.Unmarshal(resp.Result, &shot) == nil && shot.Image != "" {
		if shot.MimeType == "" {
			shot.MimeType = "image/png"
		}
		blocks := []any{map[string]any{"type": "image", "data": shot.Image, "mimeType": shot.MimeType}}
		if shot.Note != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": shot.Note})
		}
		return blocks, nil
	}
	var s string
	if json.Unmarshal(resp.Result, &s) == nil {
		return []any{map[string]any{"type": "text", "text": s}}, nil
	}
	return []any{map[string]any{"type": "text", "text": string(resp.Result)}}, nil
}
