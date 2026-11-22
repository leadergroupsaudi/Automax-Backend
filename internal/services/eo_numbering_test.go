package services

import "testing"

func TestRenderReferenceOwnSequence(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		year    int
		seq     int
		want    string
	}{
		{"four digits", "IN-YYYY-NNNN", 2026, 7, "IN-2026-0007"},
		{"three digits", "MED-YYYY-NNN", 2026, 42, "MED-2026-042"},
		{"two digits", "CMT-YYYY-NN", 2025, 3, "CMT-2025-03"},
		{"multi-segment prefix", "EO-DIR-YYYY-NNN", 2026, 1, "EO-DIR-2026-001"},
		{"overflow widens rather than truncates", "CMT-YYYY-NN", 2026, 123, "CMT-2026-123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderReference(tt.pattern, "", tt.year, tt.seq)
			if err != nil {
				t.Fatalf("RenderReference(%q) error: %v", tt.pattern, err)
			}
			if got != tt.want {
				t.Errorf("RenderReference(%q) = %q, want %q", tt.pattern, got, tt.want)
			}
		})
	}
}

func TestRenderReferenceParentDerived(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		parent  string
		seq     int
		want    string
	}{
		{"level 1", "IN-YYYY-NNNN-R#", "IN-2026-0007", 2, "IN-2026-0007-R2"},
		{"level 1 directive", "EO-DIR-YYYY-NNN-A#", "EO-DIR-2026-001", 10, "EO-DIR-2026-001-A10"},
		{"level 2", "CMT-YYYY-NN-M#-I#", "CMT-2026-03-M4", 1, "CMT-2026-03-M4-I1"},
		{"level 3 chain", "VIS-YYYY-NNN-I#-T#", "VIS-2026-012-I2", 5, "VIS-2026-012-I2-T5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// year is deliberately wrong: a derived reference takes its
			// year from the parent, never from the clock.
			got, err := RenderReference(tt.pattern, tt.parent, 1999, tt.seq)
			if err != nil {
				t.Fatalf("RenderReference(%q) error: %v", tt.pattern, err)
			}
			if got != tt.want {
				t.Errorf("RenderReference(%q, %q) = %q, want %q", tt.pattern, tt.parent, got, tt.want)
			}
		})
	}
}

func TestRenderReferenceRejectsParentModeWithoutParent(t *testing.T) {
	if _, err := RenderReference("IN-YYYY-NNNN-R#", "", 2026, 1); err == nil {
		t.Errorf("parent-derived pattern with empty parent: want error, got nil")
	}
}

func TestRenderReferenceRejectsBadInput(t *testing.T) {
	tests := []struct {
		name, pattern, parent string
		year, seq             int
	}{
		{"empty pattern", "", "", 2026, 1},
		{"zero sequence", "IN-YYYY-NNNN", "", 2026, 0},
		{"negative sequence", "IN-YYYY-NNNN", "", 2026, -1},
		{"own pattern without a sequence token", "IN-YYYY", "", 2026, 1},
		{"own pattern without a year", "IN-NNNN", "", 2026, 1},
		{"own pattern with year out of range", "IN-YYYY-NNNN", "", 99999, 1},
		{"two # in one segment", "IN-YYYY-NNNN-R##", "IN-2026-0001", 2026, 1},
		{"suffix segment without a letter", "IN-YYYY-NNNN-#", "IN-2026-0001", 2026, 1},
		{"parent at wrong depth", "CMT-YYYY-NN-M#-I#", "CMT-2026-03", 2026, 1},
		{"unrecognised segment", "IN-YYYY-NNNN-RX", "", 2026, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := RenderReference(tt.pattern, tt.parent, tt.year, tt.seq); err == nil {
				t.Errorf("RenderReference(%q, %q, %d, %d) = %q, want error", tt.pattern, tt.parent, tt.year, tt.seq, got)
			}
		})
	}
}

func TestEOSequenceScope(t *testing.T) {
	own := "IN-YYYY-NNNN"
	child := "IN-YYYY-NNNN-R#"
	tests := []struct {
		name string
		a, b string
		same bool
	}{
		{"own, different years", eoScope(t, own, "", 2025), eoScope(t, own, "", 2026), false},
		{"own, same year", eoScope(t, own, "", 2026), eoScope(t, own, "", 2026), true},
		{"child, different parents", eoScope(t, child, "IN-2026-0001", 2026), eoScope(t, child, "IN-2026-0002", 2026), false},
		{"child, same parent", eoScope(t, child, "IN-2026-0001", 2026), eoScope(t, child, "IN-2026-0001", 2026), true},
		{"different patterns, same parent", eoScope(t, "CMT-YYYY-NN-M#", "CMT-2026-03", 2026), eoScope(t, "CMT-YYYY-NN-X#", "CMT-2026-03", 2026), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if (tt.a == tt.b) != tt.same {
				t.Errorf("scopes %q and %q: equal=%v, want %v", tt.a, tt.b, tt.a == tt.b, tt.same)
			}
		})
	}
	if _, err := EOSequenceScope(child, "", 2026); err == nil {
		t.Errorf("EOSequenceScope parent-derived with empty parent: want error")
	}
}

func eoScope(t *testing.T, pattern, parent string, year int) string {
	t.Helper()
	s, err := EOSequenceScope(pattern, parent, year)
	if err != nil {
		t.Fatalf("EOSequenceScope(%q,%q,%d): %v", pattern, parent, year, err)
	}
	return s
}
