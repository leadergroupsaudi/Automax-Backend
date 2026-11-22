package database

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestMigrateReturnsTheEOPlatformMigrationError reads postgres.go's syntax
// tree and asserts that the MigrateEOPlatform call site RETURNS its error
// instead of logging it.
//
// It is a source-level test on purpose. Migrate builds the entire application
// schema against a live database, so exercising this branch for real would
// mean standing up the whole schema and then corrupting eo_records inside it —
// a far more fragile check than the one-line property actually at stake.
// migrations.MigrateEOPlatform's own failure modes are covered against a real
// database in repository.TestMigrateEOPlatformDetectsShapeDrift,
// TestMigrateEOPlatformRejectsAWrongPartitionKey and
// TestMigrateEOPlatformRejectsANonPartitionedTable; what is NOT covered there
// is whether anybody listens, and that is the entire defect: all three return
// errors precisely so production and the migration file cannot drift
// unnoticed, and a log.Printf at the call site turned every one of them into a
// warning over which the server booted — EO routes registered, seed attempted,
// reference uniqueness silently unenforced in the relkind case.
//
// The single-line mutation that makes this fail: change the call site back to
// `log.Printf("Warning: EO platform migration failed: %v", err)`.
func TestMigrateReturnsTheEOPlatformMigrationError(t *testing.T) {
	const src = "postgres.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var body []ast.Stmt
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		// The `if err := migrations.MigrateEOPlatform(db); err != nil { … }`
		// statement, located by its init call rather than by line number.
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok || ifStmt.Init == nil {
			return true
		}
		assign, ok := ifStmt.Init.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "MigrateEOPlatform" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "migrations" {
			return true
		}
		found = true
		body = ifStmt.Body.List
		return false
	})
	if !found {
		t.Fatalf("no `if err := migrations.MigrateEOPlatform(db); err != nil` statement in %s", src)
	}

	hasReturn := false
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.ReturnStmt:
			hasReturn = true
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "log" && strings.HasPrefix(sel.Sel.Name, "Print") {
				t.Errorf("%s logs the EO platform migration failure with log.%s and carries on; "+
					"MigrateEOPlatform fails CLOSED on purpose and its error must be returned", src, sel.Sel.Name)
			}
		}
	}
	if !hasReturn {
		t.Errorf("the MigrateEOPlatform failure branch in %s does not return: the server would boot over an unverified eo_records", src)
	}
}
