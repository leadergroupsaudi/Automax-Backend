package licensing

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestCatalogFeatureCodesUnique guards against accidental duplicate entries in
// the catalog — each feature code must appear exactly once.
func TestCatalogFeatureCodesUnique(t *testing.T) {
	seen := make(map[FeatureCode]int, len(Catalog))
	for _, f := range Catalog {
		if f.Code == "" {
			t.Errorf("catalog contains feature with empty code: %+v", f)
		}
		seen[f.Code]++
	}
	for code, count := range seen {
		if count > 1 {
			t.Errorf("feature code %q appears %d times in catalog — must be unique", code, count)
		}
	}
}

// TestCatalogDependenciesResolve ensures every Dependency points at a real feature code.
func TestCatalogDependenciesResolve(t *testing.T) {
	known := make(map[FeatureCode]bool, len(Catalog))
	for _, f := range Catalog {
		known[f.Code] = true
	}
	for _, f := range Catalog {
		for _, dep := range f.Dependencies {
			if !known[dep] {
				t.Errorf("feature %q depends on %q which is not in the catalog", f.Code, dep)
			}
		}
	}
}

// TestCatalogPermissionModulesAreSeeded asserts that every permission module listed
// in Catalog entries is actually seeded in postgres.go. Catches the drift scenario
// where a new feature is added but no permissions are created for its module.
//
// The test reads the seed source file and regex-extracts Module: "…" values rather
// than spinning up a test database — this keeps the check hermetic and fast.
func TestCatalogPermissionModulesAreSeeded(t *testing.T) {
	seedPath := filepath.Join("..", "database", "postgres.go")
	content, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read %s: %v", seedPath, err)
	}

	// Match: Module: "some-name"
	re := regexp.MustCompile(`Module:\s*"([a-z][a-z0-9_\-]*)"`)
	matches := re.FindAllStringSubmatch(string(content), -1)
	seeded := make(map[string]bool, len(matches))
	for _, m := range matches {
		seeded[m[1]] = true
	}

	if len(seeded) == 0 {
		t.Fatalf("found no Module: strings in %s — regex is probably stale", seedPath)
	}

	for _, f := range Catalog {
		for _, mod := range f.PermissionModules {
			if !seeded[mod] {
				t.Errorf("feature %q lists permission module %q, but no permissions with that module are seeded in %s",
					f.Code, mod, seedPath)
			}
		}
	}
}

// TestFeatureForModuleRoundTrip verifies the reverse lookup: every module in a
// feature's PermissionModules resolves back to that feature via FeatureForModule.
func TestFeatureForModuleRoundTrip(t *testing.T) {
	for _, f := range Catalog {
		for _, mod := range f.PermissionModules {
			got := FeatureForModule(mod)
			if got == nil {
				t.Errorf("FeatureForModule(%q) returned nil — expected %q", mod, f.Code)
				continue
			}
			if *got != f.Code {
				t.Errorf("FeatureForModule(%q) = %q, want %q", mod, *got, f.Code)
			}
		}
	}
}

// TestAllCodesMatchCatalog verifies AllCodes returns exactly the codes in Catalog,
// in the same order. If this fails, the dev seeder will issue licenses that don't
// match the catalog.
func TestAllCodesMatchCatalog(t *testing.T) {
	codes := AllCodes()
	if len(codes) != len(Catalog) {
		t.Fatalf("AllCodes returned %d entries, Catalog has %d", len(codes), len(Catalog))
	}
	for i, f := range Catalog {
		if codes[i] != string(f.Code) {
			t.Errorf("AllCodes[%d] = %q, Catalog[%d].Code = %q", i, codes[i], i, f.Code)
		}
	}
}

// TestFindByCode exercises the Find helper for all known codes and an unknown one.
func TestFindByCode(t *testing.T) {
	for _, f := range Catalog {
		got := Find(f.Code)
		if got == nil {
			t.Errorf("Find(%q) = nil, want non-nil", f.Code)
			continue
		}
		if got.Code != f.Code {
			t.Errorf("Find(%q).Code = %q", f.Code, got.Code)
		}
	}
	if Find("nonexistent-feature-xyz") != nil {
		t.Errorf("Find(nonexistent) should return nil")
	}
}

func TestEOPlatformFeatureIsCatalogued(t *testing.T) {
	f := Find(FeatureEOPlatform)
	if f == nil {
		t.Fatal("FeatureEOPlatform missing from Catalog")
	}
	if len(f.PermissionModules) != 1 || f.PermissionModules[0] != "eo" {
		t.Errorf("PermissionModules = %v, want [eo]", f.PermissionModules)
	}
	if got := FeatureForModule("eo"); got == nil || *got != FeatureEOPlatform {
		t.Errorf("FeatureForModule(eo) = %v, want FeatureEOPlatform", got)
	}
}
