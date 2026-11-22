package models

import (
	"testing"

	"github.com/google/uuid"
)

func TestValidateReferencePrefixRejectsDuplicate(t *testing.T) {
	existing := []EORecordType{{Key: "directive", ReferencePrefix: "EO-DIR"}}
	if err := ValidateReferencePrefix(EORecordType{ReferencePrefix: "EO-DIR"}, existing); err == nil {
		t.Error("duplicate prefix accepted; reference uniqueness is per-partition only")
	}
	if err := ValidateReferencePrefix(EORecordType{ReferencePrefix: "eo-dir"}, existing); err == nil {
		t.Error("case-variant duplicate accepted")
	}
	if err := ValidateReferencePrefix(EORecordType{ReferencePrefix: "EO-COM"}, existing); err != nil {
		t.Errorf("unique prefix rejected: %v", err)
	}
}

func TestValidateReferencePrefixRejectsPrefixOfAnother(t *testing.T) {
	existing := []EORecordType{{Key: "directive", ReferencePrefix: "EO-DIR"}}
	if err := ValidateReferencePrefix(EORecordType{ReferencePrefix: "EO"}, existing); err == nil {
		t.Error("prefix that is a prefix of an existing one accepted")
	}
	if err := ValidateReferencePrefix(EORecordType{ReferencePrefix: "EO-DIR-X"}, existing); err == nil {
		t.Error("prefix that extends an existing one accepted")
	}
	if err := ValidateReferencePrefix(EORecordType{ReferencePrefix: "eo"}, existing); err == nil {
		t.Error("lowercase prefix-of-another accepted")
	}
}

func TestValidateReferencePrefixRejectsEmpty(t *testing.T) {
	if err := ValidateReferencePrefix(EORecordType{ReferencePrefix: "  "}, nil); err == nil {
		t.Error("blank prefix accepted")
	}
}

func TestValidateReferencePrefixIgnoresTypesWithoutPrefix(t *testing.T) {
	// Abstract bases carry no prefix; an empty existing prefix must not
	// count as a prefix of everything.
	existing := []EORecordType{{Key: "base", IsAbstract: true}}
	if err := ValidateReferencePrefix(EORecordType{ReferencePrefix: "EO-DIR"}, existing); err != nil {
		t.Errorf("rejected against a type with no prefix: %v", err)
	}
}

// A parent-derived type issues every reference inside its parent's, so it owns
// no prefix of its own: a truthful prefix always overlaps its parent's and a
// blank one would be rejected. Both must pass.
func TestValidateReferencePrefixExemptsParentDerivedTypes(t *testing.T) {
	existing := []EORecordType{{Key: "directive", ReferencePrefix: "EO-DIR"}}
	derived := EORecordType{
		Key:              "assignment",
		ReferenceMode:    EOReferenceModeParent,
		ReferencePattern: "EO-DIR-YYYY-NNN-A#",
	}
	if err := ValidateReferencePrefix(derived, existing); err != nil {
		t.Errorf("a parent-derived type with no prefix was rejected: %v", err)
	}
	// Even the prefix it genuinely renders under — which overlaps its
	// parent's by construction — must not be refused.
	derived.ReferencePrefix = "EO-DIR"
	if err := ValidateReferencePrefix(derived, existing); err != nil {
		t.Errorf("a parent-derived type was rejected against its own parent's prefix: %v", err)
	}
	// The exemption is for parent-derived types ONLY: an own-sequence type is
	// still held to the rule.
	own := EORecordType{Key: "assignment", ReferenceMode: EOReferenceModeOwn, ReferencePrefix: "EO-DIR"}
	if err := ValidateReferencePrefix(own, existing); err == nil {
		t.Error("an own-sequence type was let through with an overlapping prefix")
	}
}

// An abstract type holds no records and issues no references, so it needs no
// prefix either. The rule carries that exemption so the only copy of it is not
// the handler's early return.
func TestValidateReferencePrefixExemptsAbstractTypes(t *testing.T) {
	existing := []EORecordType{{Key: "directive", ReferencePrefix: "EO-DIR"}}
	base := EORecordType{Key: "eo_base", IsAbstract: true}
	if err := ValidateReferencePrefix(base, existing); err != nil {
		t.Errorf("an abstract type with no prefix was rejected: %v", err)
	}
}

func TestEOModelTableNames(t *testing.T) {
	if got := (EORecordType{}).TableName(); got != "eo_record_types" {
		t.Errorf("EORecordType table = %q", got)
	}
	if got := (EOFieldDef{}).TableName(); got != "eo_field_defs" {
		t.Errorf("EOFieldDef table = %q", got)
	}
	if got := (EOLinkType{}).TableName(); got != "eo_link_types" {
		t.Errorf("EOLinkType table = %q", got)
	}
}

func TestEOBeforeCreateAssignsID(t *testing.T) {
	rt := &EORecordType{}
	fd := &EOFieldDef{}
	lt := &EOLinkType{}
	for _, f := range []func(*uuid.UUID) error{
		func(id *uuid.UUID) error { err := rt.BeforeCreate(nil); *id = rt.ID; return err },
		func(id *uuid.UUID) error { err := fd.BeforeCreate(nil); *id = fd.ID; return err },
		func(id *uuid.UUID) error { err := lt.BeforeCreate(nil); *id = lt.ID; return err },
	} {
		var id uuid.UUID
		if err := f(&id); err != nil || id == uuid.Nil {
			t.Errorf("BeforeCreate did not assign an ID (err=%v)", err)
		}
	}
	preset := uuid.New()
	rt2 := &EORecordType{ID: preset}
	_ = rt2.BeforeCreate(nil)
	if rt2.ID != preset {
		t.Error("BeforeCreate overwrote an existing ID")
	}
}

func eoBase() (EORecordType, []EOFieldDef) {
	return EORecordType{Key: "directive", ReferencePrefix: "EO-DIR"},
		[]EOFieldDef{
			{Key: "title", Kind: EOKindText, RequiredMode: EORequiredAlways},
			{Key: "notes", Kind: EOKindLongText, RequiredMode: EORequiredNever},
		}
}

func TestValidateTypeEditAllowsAdditiveChanges(t *testing.T) {
	before, fb := eoBase()
	fb[0].Config = []byte(`{"options":["a"]}`)
	after := before
	fa := []EOFieldDef{fb[0], fb[1], {Key: "extra", Kind: EOKindText, RequiredMode: EORequiredNever}}
	fa[0].Config = []byte(`{"options":["a","b"]}`) // new dropdown option
	if err := ValidateTypeEdit(before, after, fb, fa, true); err != nil {
		t.Errorf("additive change rejected: %v", err)
	}
	// Tightening nothing, making an optional field optional again, is fine.
	if err := ValidateTypeEdit(before, after, fb, fb, true); err != nil {
		t.Errorf("no-op edit rejected: %v", err)
	}
	// Relaxing requiredness is not destructive.
	fr := []EOFieldDef{fb[0], fb[1]}
	fr[0].RequiredMode = EORequiredNever
	if err := ValidateTypeEdit(before, after, fb, fr, true); err != nil {
		t.Errorf("relaxing requiredness rejected: %v", err)
	}
}

func TestValidateTypeEditBlocksDestructiveChangesWhenRecordsExist(t *testing.T) {
	before, fb := eoBase()

	removed := []EOFieldDef{fb[0]}
	kind := []EOFieldDef{fb[0], fb[1]}
	kind[1].Kind = EOKindText
	required := []EOFieldDef{fb[0], fb[1]}
	required[1].RequiredMode = EORequiredConditional

	cases := map[string][]EOFieldDef{"remove": removed, "kind": kind, "required": required}
	for name, fa := range cases {
		if err := ValidateTypeEdit(before, before, fb, fa, true); err == nil {
			t.Errorf("%s: destructive change allowed with records", name)
		}
		if err := ValidateTypeEdit(before, before, fb, fa, false); err != nil {
			t.Errorf("%s: change rejected with no records: %v", name, err)
		}
	}

	// A newly added required field is not an existing field being tightened,
	// but existing records would lack it; spec says only existing fields.
	added := []EOFieldDef{fb[0], fb[1], {Key: "new", Kind: EOKindText, RequiredMode: EORequiredAlways}}
	if err := ValidateTypeEdit(before, before, fb, added, true); err != nil {
		t.Errorf("new field rejected: %v", err)
	}
}

func TestValidateTypeEditTreatsReferencePatternChangeAsDestructive(t *testing.T) {
	before, fb := eoBase()
	before.ReferencePattern = "EO-DX-YYYY-NNN"
	after := before
	after.ReferencePattern = "EO-DX-YYYY-NNNN"
	// The sequence scope is keyed on the pattern, so a live type that changes
	// it starts counting at 1 again under a new scope and re-issues
	// references that are already in use.
	if err := ValidateTypeEdit(before, after, fb, fb, true); err == nil {
		t.Error("reference pattern change allowed with records")
	}
	if err := ValidateTypeEdit(before, after, fb, fb, false); err != nil {
		t.Errorf("reference pattern change rejected with no records: %v", err)
	}
}

func TestValidateTypeEditTreatsPrefixChangeAsDestructive(t *testing.T) {
	before, fb := eoBase()
	after := before
	after.ReferencePrefix = "EO-DX"
	if err := ValidateTypeEdit(before, after, fb, fb, true); err == nil {
		t.Error("prefix change allowed with records")
	}
	if err := ValidateTypeEdit(before, after, fb, fb, false); err != nil {
		t.Errorf("prefix change rejected with no records: %v", err)
	}
}

// TestValidateTypeEditTreatsIdentityAndStructureChangesAsDestructive covers
// the four attributes nothing guarded until an update ROUTE existed: before
// task 19 there was no way to change any of them, so the hole was unreachable.
//
// Each case names the mutation that re-opens it: deleting the matching guard
// from ValidateTypeEdit fails exactly the subtest named here.
func TestValidateTypeEditTreatsIdentityAndStructureChangesAsDestructive(t *testing.T) {
	oldWorkflow, newWorkflow := uuid.New(), uuid.New()
	oldParent, newParent := uuid.New(), uuid.New()

	cases := []struct {
		name string
		// mutate turns the before-state into the proposed after-state.
		mutate func(before *EORecordType, after *EORecordType)
	}{
		{
			// Guard: the WorkflowID branch. Removing it orphans every
			// existing record's state_id.
			name: "repointing the workflow",
			mutate: func(before, after *EORecordType) {
				before.WorkflowID = &oldWorkflow
				after.WorkflowID = &newWorkflow
			},
		},
		{
			name: "giving a type a workflow it did not have",
			mutate: func(before, after *EORecordType) {
				before.WorkflowID = nil
				after.WorkflowID = &newWorkflow
			},
		},
		{
			name: "taking a type's workflow away",
			mutate: func(before, after *EORecordType) {
				before.WorkflowID = &oldWorkflow
				after.WorkflowID = nil
			},
		},
		{
			// Guard: the ParentTypeID branch. Removing it lets stored values
			// lose the inherited definitions that give them meaning.
			name: "re-parenting the type",
			mutate: func(before, after *EORecordType) {
				before.ParentTypeID = &oldParent
				after.ParentTypeID = &newParent
			},
		},
		{
			name: "detaching the type from its parent",
			mutate: func(before, after *EORecordType) {
				before.ParentTypeID = &oldParent
				after.ParentTypeID = nil
			},
		},
		{
			// Guard: the Key branch.
			name:   "re-keying the type",
			mutate: func(before, after *EORecordType) { after.Key = "directive_v2" },
		},
		{
			// Guard: the IsAbstract branch.
			name: "making a populated type abstract",
			mutate: func(before, after *EORecordType) {
				before.IsAbstract = false
				after.IsAbstract = true
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, fb := eoBase()
			after := before
			tc.mutate(&before, &after)

			if err := ValidateTypeEdit(before, after, fb, fb, true); err == nil {
				t.Errorf("change allowed while records exist, want it refused")
			}
			// The rule is "not once records exist", not "never": a type
			// nobody has used yet is still being designed.
			if err := ValidateTypeEdit(before, after, fb, fb, false); err != nil {
				t.Errorf("change rejected with no records: %v", err)
			}
		})
	}
}

// The inverse of the IsAbstract rule: an abstract type that becomes concrete
// is not destructive, and nothing else here is tripped by a no-op.
func TestValidateTypeEditAllowsHarmlessIdentityEdits(t *testing.T) {
	workflow := uuid.New()
	parent := uuid.New()

	cases := []struct {
		name   string
		mutate func(before *EORecordType, after *EORecordType)
	}{
		{"making an abstract type concrete", func(before, after *EORecordType) {
			before.IsAbstract = true
			after.IsAbstract = false
		}},
		{"renaming without re-keying", func(before, after *EORecordType) {
			after.Name = "Directive (revised)"
			after.NameAr = "توجيه"
		}},
		{"keeping the same workflow and parent", func(before, after *EORecordType) {
			before.WorkflowID, after.WorkflowID = &workflow, &workflow
			before.ParentTypeID, after.ParentTypeID = &parent, &parent
		}},
		{"deactivating the type", func(before, after *EORecordType) {
			before.IsActive = true
			after.IsActive = false
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, fb := eoBase()
			after := before
			tc.mutate(&before, &after)
			if err := ValidateTypeEdit(before, after, fb, fb, true); err != nil {
				t.Errorf("harmless edit rejected: %v", err)
			}
		})
	}
}
