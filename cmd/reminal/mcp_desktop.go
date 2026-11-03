// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"reminal/internal/client"
)

// The desktop tool: read and operate apps on a machine this device owns through
// its accessibility tree (agent-desktop on that machine), headless, over
// reminal's end-to-end encrypted owner channel.

func mcpDesktopTool(obj func(map[string]any, ...string) map[string]any, str func(string) map[string]any) map[string]any {
	num := func(d string) map[string]any { return map[string]any{"type": "number", "description": d} }
	return map[string]any{
		"name": "desktop",
		"description": "Read and operate desktop apps on this machine or another machine this device owns, through " +
			"the OS accessibility tree (needs agent-desktop installed on that machine). Headless: the pointer, " +
			"keyboard focus and front window there are left alone, so the person using it is not interrupted. " +
			"action=windows lists windows; action=controls lists one window's controls (ref, role, name, value); " +
			"action=press, toggle or set acts on a ref from controls (set fills a text field with value); " +
			"action=tap presses the control at x,y, fractions 0..1 of the window. Refs go stale when the window " +
			"changes: run controls again then. Window contents travel only inside reminal's end-to-end encryption.",
		"inputSchema": obj(map[string]any{
			"action":    map[string]any{"type": "string", "enum": []string{"windows", "controls", "press", "toggle", "set", "tap"}},
			"machine":   str("Machine name or id from list_sessions; omit for this machine."),
			"window_id": str("From action=windows."),
			"ref":       str("From action=controls, e.g. @s8f3k2p9:e8."),
			"value":     str("Text for action=set."),
			"x":         num("action=tap: 0..1 from the window's left edge."),
			"y":         num("action=tap: 0..1 from the window's top edge."),
		}, "action"),
	}
}

// argFloat reads a number argument, sent as a JSON number or a string.
func argFloat(args map[string]any, key string) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

func mcpDesktop(args map[string]any) (string, error) {
	var pub ed25519.PublicKey
	if sel := strings.TrimSpace(argStr(args, "machine", "")); sel != "" && !machineLooksLocal(sel) {
		om, err := client.ResolveOwnedMachine(sel)
		if err != nil {
			return "", err
		}
		pub = om.Key
	}
	action := argStr(args, "action", "")
	if action == "windows" {
		wins, err := client.DesktopWindows(pub)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		b.WriteString("window_id\tapp\ttitle\tsize\n")
		for _, w := range wins {
			fmt.Fprintf(&b, "%s\t%s\t%s\t%dx%d\n", w.ID, w.App, w.Title, w.W, w.H)
		}
		return b.String(), nil
	}
	req := client.DesktopRequest{ID: argStr(args, "window_id", "")}
	if req.ID == "" {
		return "", errors.New("window_id is required (action=windows lists them)")
	}
	switch action {
	case "controls":
		req.Op = "tree"
	case "press", "toggle", "set":
		req.Op, req.Ref = "act", argStr(args, "ref", "")
		req.Action = map[string]string{"press": "click", "toggle": "toggle", "set": "set-value"}[action]
		req.Value = argStr(args, "value", "")
	case "tap":
		req.Op = "tap"
		req.X, req.Y = argFloat(args, "x"), argFloat(args, "y")
	default:
		return "", errors.New("action must be windows, controls, press, toggle, set or tap")
	}
	rep, err := client.Desktop(pub, req)
	if err != nil {
		return "", err
	}
	if rep.Error != "" && !rep.Pixel {
		return "", errors.New(rep.Error)
	}
	switch {
	case action == "controls":
		var b strings.Builder
		fmt.Fprintf(&b, "%d controls (%d ms)\nref\trole\tname\tvalue\n", len(rep.Els), rep.Ms)
		for _, e := range rep.Els {
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", e.Ref, e.Role, e.Name, e.Value)
		}
		return b.String(), nil
	case rep.Pixel:
		return "No control at that point; nothing was pressed.", nil
	case rep.Hit != nil:
		return fmt.Sprintf("%s: %s %q (%d ms)", rep.Did, rep.Hit.Role, rep.Hit.Name, rep.Ms), nil
	}
	return fmt.Sprintf("%s done (%d ms)", rep.Did, rep.Ms), nil
}
