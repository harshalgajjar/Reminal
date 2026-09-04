// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestEveryAgentRunPathReapsOrphans guards the bug this test was written for:
// keepawake.ReapOrphans existed, was documented as the fix for hot-restart
// inhibitor leaks, and was wired into the two COLD-start paths — where it can
// never find anything, because a genuinely new PID has no `-w <ourpid>`
// orphans. The hot-restart resume path, the only one that keeps our PID and so
// the only one with orphans to reap, did not call it. Every restart leaked one
// caffeinate per agent; an auto-update leaked one per agent on the machine at
// once, and they accumulate for as long as the process lives.
//
// The defect was wiring, not logic, so this asserts the wiring: any branch of
// main() that starts an agent must reap first. Source-level because the thing
// that went wrong is "a code path forgot to call it", which no unit test of
// ReapOrphans itself can catch.
func TestEveryAgentRunPathReapsOrphans(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}

	var mainFn *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "main" && fn.Recv == nil {
			mainFn = fn
		}
	}
	if mainFn == nil {
		t.Fatal("no func main in main.go")
	}

	// Every statement list that calls agent.Run() must also call
	// keepawake.ReapOrphans(), and the reap must come first — reaping after
	// Start()/agent.Run() would kill the inhibitor we just took out.
	var check func(stmts []ast.Stmt)
	seen := 0
	check = func(stmts []ast.Stmt) {
		runLine, reapLine := 0, 0
		for _, st := range stmts {
			ast.Inspect(st, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch {
				case id.Name == "agent" && sel.Sel.Name == "Run":
					if runLine == 0 {
						runLine = fset.Position(call.Pos()).Line
					}
				case id.Name == "keepawake" && sel.Sel.Name == "ReapOrphans":
					if reapLine == 0 {
						reapLine = fset.Position(call.Pos()).Line
					}
				}
				return true
			})
		}
		if runLine != 0 {
			seen++
			if reapLine == 0 {
				t.Errorf("main.go:%d: this branch starts an agent but never calls "+
					"keepawake.ReapOrphans() — a hot restart into it leaks the previous "+
					"image's caffeinate inhibitors, one per restart, forever", runLine)
			} else if reapLine > runLine {
				t.Errorf("main.go:%d: keepawake.ReapOrphans() is called at line %d, AFTER "+
					"agent.Run() at %d — it must run first or it kills our own fresh inhibitor",
					runLine, reapLine, runLine)
			}
		}
		// Recurse into nested blocks so each if/else branch is judged alone.
		for _, st := range stmts {
			switch s := st.(type) {
			case *ast.IfStmt:
				check(s.Body.List)
				if s.Else != nil {
					if b, ok := s.Else.(*ast.BlockStmt); ok {
						check(b.List)
					} else if e, ok := s.Else.(*ast.IfStmt); ok {
						check([]ast.Stmt{e})
					}
				}
			case *ast.BlockStmt:
				check(s.List)
			case *ast.CaseClause:
				check(s.Body)
			case *ast.SwitchStmt:
				check(s.Body.List)
			}
		}
	}
	check(mainFn.Body.List)

	if seen == 0 {
		t.Fatal("found no agent.Run() call in main() — this test has gone stale " +
			"and is no longer guarding anything")
	}
}
