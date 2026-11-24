package models

import (
	"strings"
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
	if err := ValidateTypeEdit(before, after, fb, fa, true, nil); err != nil {
		t.Errorf("additive change rejected: %v", err)
	}
	// Tightening nothing, making an optional field optional again, is fine.
	if err := ValidateTypeEdit(before, after, fb, fb, true, nil); err != nil {
		t.Errorf("no-op edit rejected: %v", err)
	}
	// Relaxing requiredness is not destructive.
	fr := []EOFieldDef{fb[0], fb[1]}
	fr[0].RequiredMode = EORequiredNever
	if err := ValidateTypeEdit(before, after, fb, fr, true, nil); err != nil {
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
		if err := ValidateTypeEdit(before, before, fb, fa, true, nil); err == nil {
			t.Errorf("%s: destructive change allowed with records", name)
		}
		if err := ValidateTypeEdit(before, before, fb, fa, false, nil); err != nil {
			t.Errorf("%s: change rejected with no records: %v", name, err)
		}
	}

	// A newly added required field is not an existing field being tightened,
	// but existing records would lack it; spec says only existing fields.
	added := []EOFieldDef{fb[0], fb[1], {Key: "new", Kind: EOKindText, RequiredMode: EORequiredAlways}}
	if err := ValidateTypeEdit(before, before, fb, added, true, nil); err != nil {
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
	if err := ValidateTypeEdit(before, after, fb, fb, true, nil); err == nil {
		t.Error("reference pattern change allowed with records")
	}
	if err := ValidateTypeEdit(before, after, fb, fb, false, nil); err != nil {
		t.Errorf("reference pattern change rejected with no records: %v", err)
	}
}

func TestValidateTypeEditTreatsPrefixChangeAsDestructive(t *testing.T) {
	before, fb := eoBase()
	after := before
	after.ReferencePrefix = "EO-DX"
	if err := ValidateTypeEdit(before, after, fb, fb, true, nil); err == nil {
		t.Error("prefix change allowed with records")
	}
	if err := ValidateTypeEdit(before, after, fb, fb, false, nil); err != nil {
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

			if err := ValidateTypeEdit(before, after, fb, fb, true, nil); err == nil {
				t.Errorf("change allowed while records exist, want it refused")
			}
			// The rule is "not once records exist", not "never": a type
			// nobody has used yet is still being designed.
			if err := ValidateTypeEdit(before, after, fb, fb, false, nil); err != nil {
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
			if err := ValidateTypeEdit(before, after, fb, fb, true, nil); err != nil {
				t.Errorf("harmless edit rejected: %v", err)
			}
		})
	}
}

// ── §7.14's confirmed destructive edit ──────────────────────────────────────

// eoDestructiveCase is one of the destructive changes ValidateTypeEdit refuses
// while records exist, together with the confirmation that authorises exactly
// it. The table is shared by every test below so that the no-confirmation
// behaviour, the acceptance and the three mismatch refusals are all stated
// over THE SAME set of edits, and a guard that disappears from one of them
// cannot keep passing in the others.
type eoDestructiveCase struct {
	name string
	// change is the identifier a confirmation names, and want is a fragment
	// of the refusal the edit produces with no confirmation.
	change   string
	fieldKey string
	want     string
	// edit turns the base type and its fields into the proposed after-state.
	// It mutates before as well, because several of these changes are only
	// expressible as a difference from a before-state the base does not have
	// (a workflow to repoint, a parent to detach).
	edit func(before, after *EORecordType, fieldsAfter []EOFieldDef) []EOFieldDef
}

func eoDestructiveCases() []eoDestructiveCase {
	oldWorkflow, newWorkflow := uuid.New(), uuid.New()
	oldParent, newParent := uuid.New(), uuid.New()
	return []eoDestructiveCase{
		{
			name:   "repointing the workflow",
			change: EODestructiveWorkflow,
			want:   "changing workflow",
			edit: func(before, after *EORecordType, fa []EOFieldDef) []EOFieldDef {
				before.WorkflowID, after.WorkflowID = &oldWorkflow, &newWorkflow
				return fa
			},
		},
		{
			name:   "re-parenting the type",
			change: EODestructiveParentType,
			want:   "changing parent type",
			edit: func(before, after *EORecordType, fa []EOFieldDef) []EOFieldDef {
				before.ParentTypeID, after.ParentTypeID = &oldParent, &newParent
				return fa
			},
		},
		{
			name:   "re-keying the type",
			change: EODestructiveKey,
			want:   "changing key",
			edit: func(before, after *EORecordType, fa []EOFieldDef) []EOFieldDef {
				after.Key = "directive_v2"
				return fa
			},
		},
		{
			name:   "making a populated type abstract",
			change: EODestructiveAbstract,
			want:   "abstract is destructive",
			edit: func(before, after *EORecordType, fa []EOFieldDef) []EOFieldDef {
				before.IsAbstract, after.IsAbstract = false, true
				return fa
			},
		},
		{
			name:   "changing the reference prefix",
			change: EODestructiveReferencePrefix,
			want:   "changing reference prefix",
			edit: func(before, after *EORecordType, fa []EOFieldDef) []EOFieldDef {
				after.ReferencePrefix = "EO-DX"
				return fa
			},
		},
		{
			name:   "changing the reference pattern",
			change: EODestructiveReferencePattern,
			want:   "changing reference pattern",
			edit: func(before, after *EORecordType, fa []EOFieldDef) []EOFieldDef {
				before.ReferencePattern, after.ReferencePattern = "EO-DIR-YYYY-NNN", "EO-DIR-YYYY-NNNN"
				return fa
			},
		},
		{
			name:     "removing a field",
			change:   EODestructiveFieldRemoved,
			fieldKey: "notes",
			want:     `removing field "notes"`,
			edit: func(_, _ *EORecordType, fa []EOFieldDef) []EOFieldDef {
				return fa[:1]
			},
		},
		{
			name:     "changing a field's kind",
			change:   EODestructiveFieldKind,
			fieldKey: "notes",
			want:     `changing kind of field "notes"`,
			edit: func(_, _ *EORecordType, fa []EOFieldDef) []EOFieldDef {
				fa[1].Kind = EOKindText
				return fa
			},
		},
		{
			// The blocker this task exists for: six pilot file fields were
			// relaxed to `never` while Documenta was off, and `never ->
			// at_state` is what restores one.
			name:     "making a field required",
			change:   EODestructiveFieldRequired,
			fieldKey: "notes",
			want:     `making field "notes" required`,
			edit: func(_, _ *EORecordType, fa []EOFieldDef) []EOFieldDef {
				fa[1].RequiredMode = EORequiredAtState
				return fa
			},
		},
	}
}

// eoDestructiveEditStates builds the four arguments ValidateTypeEdit takes for
// one case.
func eoDestructiveEditStates(tc eoDestructiveCase) (before, after EORecordType, fieldsBefore, fieldsAfter []EOFieldDef) {
	before, fieldsBefore = eoBase()
	after = before
	fieldsAfter = append([]EOFieldDef{}, fieldsBefore...)
	fieldsAfter = tc.edit(&before, &after, fieldsAfter)
	return before, after, fieldsBefore, fieldsAfter
}

// eoConfirmFor is the confirmation that authorises tc exactly: the type it is
// edited on, the field it touches, the change it makes, and a stated rule.
func eoConfirmFor(tc eoDestructiveCase) *EODestructiveConfirmation {
	return &EODestructiveConfirmation{
		TypeKey:  "directive",
		FieldKey: tc.fieldKey,
		Change:   tc.change,
		Reason:   "the client's matrix demands this document; existing records are completed by the registry clerk before the next transition",
	}
}

// TestValidateTypeEditWithoutConfirmationIsUnchanged pins the SET of refusals,
// not a sample of it. Every destructive change still refuses with no
// confirmation, still allows itself when the type holds no records, and the
// table is checked against models' own list of change identifiers in both
// directions — so deleting a guard fails its named subtest, and deleting a
// guard AND its identifier fails the set assertion instead of quietly
// shrinking the thing under test.
func TestValidateTypeEditWithoutConfirmationIsUnchanged(t *testing.T) {
	cases := eoDestructiveCases()

	covered := map[string]bool{}
	for _, tc := range cases {
		covered[tc.change] = true
	}
	for _, change := range EODestructiveChanges {
		if !covered[change] {
			t.Errorf("destructive change %q has no case here: either it is a new guard with no test, or the table was not updated with it", change)
		}
	}
	known := map[string]bool{}
	for _, change := range EODestructiveChanges {
		known[change] = true
	}
	for _, tc := range cases {
		if !known[tc.change] {
			t.Errorf("case %q names change %q, which models.EODestructiveChanges does not list: a guard was removed and this test kept passing on a name nothing produces", tc.name, tc.change)
		}
	}
	if len(cases) != len(EODestructiveChanges) {
		t.Errorf("%d cases for %d destructive changes: the table and the guard set have drifted", len(cases), len(EODestructiveChanges))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, after, fb, fa := eoDestructiveEditStates(tc)

			err := ValidateTypeEdit(before, after, fb, fa, true, nil)
			if err == nil {
				t.Fatalf("destructive change allowed with no confirmation while records exist")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal = %q, want it to contain %q — the message an operator reads changed", err.Error(), tc.want)
			}
			if err := ValidateTypeEdit(before, after, fb, fa, false, nil); err != nil {
				t.Errorf("change rejected with no records: %v", err)
			}
		})
	}
}

// TestValidateTypeEditAcceptsAMatchingConfirmation asserts the specific edit
// each confirmation was given, in both directions: the named change goes
// through, AND the same edit with no confirmation is still refused. Without
// that second half the test would pass just as well against a validator that
// had stopped refusing anything at all.
func TestValidateTypeEditAcceptsAMatchingConfirmation(t *testing.T) {
	for _, tc := range eoDestructiveCases() {
		t.Run(tc.name, func(t *testing.T) {
			before, after, fb, fa := eoDestructiveEditStates(tc)

			if err := ValidateTypeEdit(before, after, fb, fa, true, nil); err == nil {
				t.Fatalf("this edit is not destructive without a confirmation, so accepting it with one proves nothing")
			}
			if err := ValidateTypeEdit(before, after, fb, fa, true, eoConfirmFor(tc)); err != nil {
				t.Fatalf("a confirmation naming type %q, field %q and change %q did not authorise it: %v", "directive", tc.fieldKey, tc.change, err)
			}
		})
	}
}

// TestValidateTypeEditRefusesAConfirmationWithNoReason: §7.14 asks for
// "explicit confirmation AND a stated backfill rule". A confirmation with no
// reason is the click without the rule, and it authorises nothing.
func TestValidateTypeEditRefusesAConfirmationWithNoReason(t *testing.T) {
	tc := eoDestructiveCases()[8] // making a field required
	if tc.change != EODestructiveFieldRequired {
		t.Fatalf("case 8 is %q, not the required-mode case this test is about", tc.change)
	}
	before, after, fb, fa := eoDestructiveEditStates(tc)

	for _, reason := range []string{"", "   \n\t"} {
		confirm := eoConfirmFor(tc)
		confirm.Reason = reason
		err := ValidateTypeEdit(before, after, fb, fa, true, confirm)
		if err == nil {
			t.Fatalf("reason %q accepted: a confirmation with no stated rule authorised a destructive edit", reason)
		}
		if !strings.Contains(err.Error(), "backfill rule") {
			t.Errorf("refusal = %q, want it to say the confirmation must state a backfill rule", err.Error())
		}
	}
}

// TestValidateTypeEditRefusesAConfirmationNamingADifferentField is the
// review-focus case: a confirmation for field A must not authorise a
// destructive change to field B. A confirmation that does not name what it
// authorises is a blanket override with extra steps.
func TestValidateTypeEditRefusesAConfirmationNamingADifferentField(t *testing.T) {
	for _, tc := range eoDestructiveCases() {
		if tc.fieldKey == "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			before, after, fb, fa := eoDestructiveEditStates(tc)

			confirm := eoConfirmFor(tc)
			confirm.FieldKey = "title" // the other field on this type, untouched by the edit
			err := ValidateTypeEdit(before, after, fb, fa, true, confirm)
			if err == nil {
				t.Fatalf("a confirmation naming field %q authorised a destructive change to field %q", confirm.FieldKey, tc.fieldKey)
			}
			if !strings.Contains(err.Error(), tc.fieldKey) || !strings.Contains(err.Error(), confirm.FieldKey) {
				t.Errorf("refusal = %q, want it to name both the confirmed field and the changed one", err.Error())
			}

			// The same refusal in the other direction: a type-level change
			// carries no field, and a confirmation that names one does not
			// authorise it.
			confirm = eoConfirmFor(tc)
			confirm.FieldKey = ""
			if err := ValidateTypeEdit(before, after, fb, fa, true, confirm); err == nil {
				t.Errorf("a confirmation naming no field authorised a destructive change to field %q", tc.fieldKey)
			}
		})
	}
}

// TestValidateTypeEditRefusesAConfirmationNamingADifferentType closes the hole
// the handler's own reading opens: hasRecords is computed over the type AND
// EVERY TYPE BENEATH IT, so an edit to an abstract ancestor is guarded by its
// descendants' records. A confirmation that did not name a type would let one
// aimed at the child authorise a change to the parent that no one reviewed.
func TestValidateTypeEditRefusesAConfirmationNamingADifferentType(t *testing.T) {
	for _, tc := range eoDestructiveCases() {
		t.Run(tc.name, func(t *testing.T) {
			before, after, fb, fa := eoDestructiveEditStates(tc)

			confirm := eoConfirmFor(tc)
			confirm.TypeKey = "eo_base" // the ancestor, not the type being edited
			err := ValidateTypeEdit(before, after, fb, fa, true, confirm)
			if err == nil {
				t.Fatalf("a confirmation naming record type %q authorised a destructive change to %q", confirm.TypeKey, before.Key)
			}
			if !strings.Contains(err.Error(), "eo_base") || !strings.Contains(err.Error(), before.Key) {
				t.Errorf("refusal = %q, want it to name both the confirmed type and the edited one", err.Error())
			}
		})
	}
}

// TestValidateTypeEditRefusesAConfirmationNamingADifferentChange: naming the
// type and the field is not enough. A confirmation obtained for removing a
// field must not authorise silently re-kinding it instead — the operator was
// shown one consequence and asked about one consequence.
func TestValidateTypeEditRefusesAConfirmationNamingADifferentChange(t *testing.T) {
	for _, tc := range eoDestructiveCases() {
		t.Run(tc.name, func(t *testing.T) {
			before, after, fb, fa := eoDestructiveEditStates(tc)

			other := EODestructiveFieldRemoved
			if tc.change == other {
				other = EODestructiveFieldKind
			}
			confirm := eoConfirmFor(tc)
			confirm.Change = other
			err := ValidateTypeEdit(before, after, fb, fa, true, confirm)
			if err == nil {
				t.Fatalf("a confirmation for %q authorised %q", other, tc.change)
			}
			if !strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), tc.change) {
				t.Errorf("refusal = %q, want it to name both the confirmed change and the one the edit makes", err.Error())
			}

			// An identifier no guard produces is refused too, rather than
			// being compared and silently failing to match.
			confirm = eoConfirmFor(tc)
			confirm.Change = "everything"
			if err := ValidateTypeEdit(before, after, fb, fa, true, confirm); err == nil {
				t.Errorf("a confirmation naming an unknown change authorised %q", tc.change)
			}
		})
	}
}

// TestValidateTypeEditRefusesAConfirmationForAnEditThatMakesTwoChanges: one
// confirmation authorises ONE change. An edit that makes two would otherwise
// have its first change confirmed and its second applied under that
// confirmation, which is the silent destruction §7.14 forbids.
func TestValidateTypeEditRefusesAConfirmationForAnEditThatMakesTwoChanges(t *testing.T) {
	before, fb := eoBase()
	before.ReferencePattern = "EO-DIR-YYYY-NNN"
	after := before
	after.ReferencePrefix = "EO-DX"
	after.ReferencePattern = "EO-DX-YYYY-NNN"

	confirm := &EODestructiveConfirmation{
		TypeKey: "directive",
		Change:  EODestructiveReferencePrefix,
		Reason:  "the office renumbered the register",
	}
	err := ValidateTypeEdit(before, after, fb, fb, true, confirm)
	if err == nil {
		t.Fatalf("a confirmation for the prefix also carried the pattern change through")
	}
	if !strings.Contains(err.Error(), EODestructiveReferencePrefix) || !strings.Contains(err.Error(), EODestructiveReferencePattern) {
		t.Errorf("refusal = %q, want it to name both changes so the operator knows what to split", err.Error())
	}

	// Two changes to the SAME field, which one PUT can also express.
	twoOnOneField := append([]EOFieldDef{}, fb...)
	twoOnOneField[1].Kind = EOKindText
	twoOnOneField[1].RequiredMode = EORequiredAlways
	confirm = &EODestructiveConfirmation{
		TypeKey:  "directive",
		FieldKey: "notes",
		Change:   EODestructiveFieldKind,
		Reason:   "the client's matrix changed the answer type",
	}
	if err := ValidateTypeEdit(before, before, fb, twoOnOneField, true, confirm); err == nil {
		t.Errorf("a confirmation for the kind change also carried the requiredness change through")
	}
}

// A confirmation is refused before anything else when it is malformed — it
// names no type, or no change at all. Both are reachable from a client that
// sends the block with an empty field, and both are refused rather than
// ignored: an unread confirmation in a request body is indistinguishable, to
// the operator who typed it, from one that was honoured.
func TestValidateTypeEditRefusesAMalformedConfirmation(t *testing.T) {
	tc := eoDestructiveCases()[8]
	before, after, fb, fa := eoDestructiveEditStates(tc)

	cases := map[string]func(*EODestructiveConfirmation){
		"no type":   func(cf *EODestructiveConfirmation) { cf.TypeKey = "" },
		"no change": func(cf *EODestructiveConfirmation) { cf.Change = "" },
		// An identifier no guard produces. It would be refused by the
		// comparison against the edit anyway — but only on an edit there is
		// something to compare it to, so without this case the check that
		// rejects it outright is unpinned.
		"an unknown change": func(cf *EODestructiveConfirmation) { cf.Change = "everything" },
		// Bounded because the stated rule is rendered into
		// models.ActionLog.Description, which is varchar(500). An unbounded
		// one reaches the audit INSERT that shares the edit's transaction,
		// and a value too long for the column rolls the edit back with an
		// internal error rather than refusing the request.
		"an overlong reason": func(cf *EODestructiveConfirmation) { cf.Reason = strings.Repeat("ب", 1001) },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			confirm := eoConfirmFor(tc)
			break_(confirm)
			if err := ValidateTypeEdit(before, after, fb, fa, true, confirm); err == nil {
				t.Fatalf("a malformed confirmation authorised a destructive edit")
			}
			// It is refused even where there is nothing to confirm: a
			// malformed confirmation is a malformed REQUEST, and answering it
			// with a success tells the operator their override was read.
			if err := ValidateTypeEdit(before, before, fb, fb, true, confirm); err == nil {
				t.Errorf("a malformed confirmation was ignored on a harmless edit")
			}
		})
	}
}
