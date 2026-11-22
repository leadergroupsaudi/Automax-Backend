package models

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestEOFieldKindsCoversEveryKindConstant is what makes EOFieldKinds an
// allow-list "built from the constants themselves" rather than a second
// hand-written list that drifts.
//
// EOFieldKinds names each EOKind* constant by identifier, so a change to a
// kind's VALUE travels automatically. The one drift a Go slice cannot prevent
// is declaring a new EOKind* constant and not adding it here — constants are
// not enumerable at runtime. So this parses the source and asserts the two
// sets are equal.
//
// The single-line mutation that makes it fail: delete any line from
// EOFieldKinds, or add an EOKind* constant without listing it.
func TestEOFieldKindsCoversEveryKindConstant(t *testing.T) {
	const src = "eo_record_type.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	declared := map[string]string{} // constant name -> its string value
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if len(name.Name) < 6 || name.Name[:6] != "EOKind" {
					continue
				}
				if i >= len(vs.Values) {
					t.Fatalf("%s has no value", name.Name)
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s is not a string literal", name.Name)
				}
				declared[name.Name] = lit.Value[1 : len(lit.Value)-1]
			}
		}
	}
	if len(declared) == 0 {
		t.Fatalf("found no EOKind* constants in %s: the parse is looking in the wrong place", src)
	}

	inList := map[string]bool{}
	for _, k := range EOFieldKinds {
		if inList[k] {
			t.Errorf("EOFieldKinds lists %q twice", k)
		}
		inList[k] = true
	}

	for name, value := range declared {
		if !inList[value] {
			t.Errorf("constant %s (%q) is not in EOFieldKinds: the designer would reject a field kind the model declares", name, value)
		}
		if !IsEOFieldKind(value) {
			t.Errorf("IsEOFieldKind(%q) = false, want true", value)
		}
		delete(inList, value)
	}
	for leftover := range inList {
		t.Errorf("EOFieldKinds contains %q, which no EOKind* constant declares", leftover)
	}

	if len(declared) != len(EOFieldKinds) {
		t.Errorf("EOFieldKinds has %d entries, %s declares %d EOKind* constants", len(EOFieldKinds), src, len(declared))
	}

	if IsEOFieldKind("") || IsEOFieldKind("drop_down") {
		t.Error("IsEOFieldKind accepted a kind nothing declares")
	}
}
