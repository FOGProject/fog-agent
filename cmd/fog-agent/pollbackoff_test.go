package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Every path out of the poll switch has to reach the wait at the bottom of
// the run loop, unless it changed something first.
//
// The run loop ends in waitOrFire. A `continue` inside the poll switch jumps
// straight past it, so the branch it sits in retries as fast as the network
// answers. That is fine for a branch that CHANGED something -- dropping the
// certificate sends the next iteration down the enrollment path, which is a
// different request with its own waiting -- and it is a hot loop for a branch
// that did not.
//
// The one that did not: a 401 that is not about this binding keeps the
// certificate and retries the identical request. On telliottwin11, during a
// six-minute window when another session had the webroot mid-deploy, that
// wrote 15,100 identical log lines -- 1.37 MB, all stamped the same second --
// and the agent made no further progress afterward.
//
// So the invariant is not "no continue". It is: a continue must be
// accompanied by a state change in the same branch. That is checkable, and it
// keeps working when someone adds the next case to this switch.
func TestEveryPollBranchThatSkipsTheWaitChangesSomething(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}

	run := findFunc(file, "runAgent")
	if run == nil {
		t.Fatal("runAgent not found in main.go; this guard is anchored on it")
	}

	sw := findPollSwitch(run)
	if sw == nil {
		t.Fatal("the poll result switch (the one handling enroll.ErrUnauthorized) " +
			"was not found; if it was renamed or restructured, re-point this guard " +
			"rather than deleting it")
	}

	checked := 0
	for _, stmt := range sw.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		checked++
		// Scoped to the block each continue actually sits in, NOT to the
		// whole clause. The ErrUnauthorized clause holds both branches --
		// keep-and-retry and drop-and-re-enroll -- so a clause-wide search
		// for DropIssued is satisfied by the drop branch and passes the
		// hot-looping one straight through. That is not a hypothetical: it
		// is how the first version of this guard failed to catch the bug it
		// was written for.
		for _, pos := range continuesWithoutAStateChange(clause) {
			t.Errorf(
				"main.go:%d: this poll branch continues the run loop "+
					"without changing anything, so it skips waitOrFire "+
					"and retries the identical request as fast as the "+
					"network answers. Use break (which leaves the switch, "+
					"not the loop) so it reaches the wait like every "+
					"other outcome.",
				fset.Position(pos).Line,
			)
		}
	}

	// A guard that inspected no branches passes for the wrong reason.
	if checked < 3 {
		t.Fatalf("only %d case clause(s) inspected; the poll switch had four "+
			"when this was written, so the scan is not reaching them", checked)
	}
}

// continuesWithoutAStateChange returns the position of every continue in the
// clause whose own innermost enclosing block does not also change state.
//
// "Its own block" is the whole point. A continue guarded by `if !drop { ... }`
// is judged on what is inside those braces, not on what a sibling branch
// further down the same case happens to do.
func continuesWithoutAStateChange(clause *ast.CaseClause) []token.Pos {
	var bad []token.Pos
	// The stack of blocks enclosing the node currently being visited.
	var stack []*ast.BlockStmt
	// The clause body is itself a block for this purpose, though the AST
	// does not model it as one.
	synthetic := &ast.BlockStmt{List: clause.Body}

	var walk func(n ast.Node)
	walk = func(n ast.Node) {
		ast.Inspect(n, func(node ast.Node) bool {
			switch v := node.(type) {
			case *ast.BlockStmt:
				stack = append(stack, v)
				for _, s := range v.List {
					walk(s)
				}
				stack = stack[:len(stack)-1]
				return false
			case *ast.BranchStmt:
				if v.Tok != token.CONTINUE {
					return true
				}
				enclosing := synthetic
				if len(stack) > 0 {
					enclosing = stack[len(stack)-1]
				}
				if !changesState(enclosing) {
					bad = append(bad, v.Pos())
				}
				return true
			}
			return true
		})
	}
	for _, s := range clause.Body {
		walk(s)
	}
	return bad
}

// changesState reports whether a block drops the issued certificate, which is
// what makes the next loop iteration a different request rather than a repeat
// of the one that just failed.
func changesState(block *ast.BlockStmt) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && sel.Sel != nil && sel.Sel.Name == "DropIssued" {
			found = true
		}
		return true
	})
	return found
}

// findFunc returns the named top-level function, or nil.
func findFunc(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name != nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// findPollSwitch returns the switch statement that handles the poll result,
// identified by the branch that names ErrUnauthorized rather than by
// position, so reordering the cases does not silently unanchor the guard.
func findPollSwitch(fn *ast.FuncDecl) *ast.SwitchStmt {
	var found *ast.SwitchStmt
	ast.Inspect(fn, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || found != nil {
			return true
		}
		for _, stmt := range sw.Body.List {
			clause, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range clause.List {
				if strings.Contains(exprText(expr), "ErrUnauthorized") {
					found = sw
					return false
				}
			}
		}
		return true
	})
	return found
}

// exprText renders an expression's identifiers as a string, enough to match
// a sentinel name inside a call like errors.Is(err, enroll.ErrUnauthorized).
func exprText(expr ast.Expr) string {
	var b strings.Builder
	ast.Inspect(expr, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			b.WriteString(id.Name)
			b.WriteString(" ")
		}
		return true
	})
	return b.String()
}
