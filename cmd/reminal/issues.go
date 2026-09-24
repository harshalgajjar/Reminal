// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

//	reminal issues            the problems agents on this machine reported with reminal
//	reminal issues export     the same as text, ready to paste into a bug report
//	reminal issues clear      forget them

import (
	"fmt"
	"strings"
	"time"

	"reminal/internal/client"
)

func runIssues(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
	case "clear":
		n, err := client.ClearIssues()
		if err != nil {
			return err
		}
		fmt.Printf("forgot %d report(s)\n", n)
		return nil
	case "", "list", "export":
	default:
		return fmt.Errorf("usage: reminal issues [export|clear]")
	}
	issues, _, err := client.ListIssues()
	if err != nil {
		return err
	}
	if len(issues) == 0 {
		fmt.Println("no reports — agents here have not reported a problem with reminal (they do it with the report_issue tool)")
		return nil
	}
	if sub == "export" {
		fmt.Printf("# reminal: %d problem(s) reported by agents on this machine\n\n", len(issues))
		for _, is := range issues {
			fmt.Println(client.IssueMarkdown(is))
		}
		return nil
	}
	for _, is := range issues {
		where := is.Session
		if is.Harness != "" {
			where += " · " + is.Harness
		}
		if is.Version != "" {
			where += " · reminal " + is.Version
		}
		fmt.Printf("%s  %-40s  %s\n", is.At.Local().Format(time.DateTime), clip(is.Title, 40), where)
	}
	fmt.Println("\n`reminal issues export` prints them in full, as a bug report to paste; `reminal issues clear` forgets them.")
	return nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
