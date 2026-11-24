package models

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// Fixed ids so the pinned sentences below are literal strings.
var (
	eoPinOldWorkflow = uuid.MustParse("aac7dd49-e892-4c80-bc1a-1c478c5058e4")
	eoPinNewWorkflow = uuid.MustParse("a6feadda-b35e-4f07-a7cc-74d33d8410bb")
	eoPinOldParent   = uuid.MustParse("f7f335ae-e891-4ccc-b484-f3c2519b4a8e")
	eoPinNewParent   = uuid.MustParse("f8ab0016-e375-417c-b9ac-aaa9ec1db2e2")
)

type eoRefusalCase struct {
	name     string
	change   string
	fieldKey string
	edit     func(before, after *EORecordType, fa []EOFieldDef) []EOFieldDef
	// message is the sentence the operator is shown TODAY. The frontend's
	// refusal banner discriminates on it, so it must not drift.
	message string
	key     string
	args    map[string]string
}

func eoRefusalCases() []eoRefusalCase {
	return []eoRefusalCase{
		{
			name: "workflow", change: EODestructiveWorkflow,
			edit: func(b, a *EORecordType, fa []EOFieldDef) []EOFieldDef {
				b.WorkflowID, a.WorkflowID = &eoPinOldWorkflow, &eoPinNewWorkflow
				return fa
			},
			message: "changing workflow from aac7dd49-e892-4c80-bc1a-1c478c5058e4 to a6feadda-b35e-4f07-a7cc-74d33d8410bb is destructive: every existing record's state_id names a state of the old workflow, and no transition runs from a state the new workflow does not contain",
			key:     "eo_destructive_workflow",
			args:    map[string]string{"from": "aac7dd49-e892-4c80-bc1a-1c478c5058e4", "to": "a6feadda-b35e-4f07-a7cc-74d33d8410bb"},
		},
		{
			name: "workflow from none", change: EODestructiveWorkflow,
			edit: func(b, a *EORecordType, fa []EOFieldDef) []EOFieldDef {
				a.WorkflowID = &eoPinNewWorkflow
				return fa
			},
			message: "changing workflow from none to a6feadda-b35e-4f07-a7cc-74d33d8410bb is destructive: every existing record's state_id names a state of the old workflow, and no transition runs from a state the new workflow does not contain",
			key:     "eo_destructive_workflow",
			// "none" is the English word the sentence uses; an empty value lets
			// the client render its own "no workflow" wording.
			args: map[string]string{"from": "", "to": "a6feadda-b35e-4f07-a7cc-74d33d8410bb"},
		},
		{
			name: "parent_type", change: EODestructiveParentType,
			edit: func(b, a *EORecordType, fa []EOFieldDef) []EOFieldDef {
				b.ParentTypeID, a.ParentTypeID = &eoPinOldParent, &eoPinNewParent
				return fa
			},
			message: "changing parent type from f7f335ae-e891-4ccc-b484-f3c2519b4a8e to f8ab0016-e375-417c-b9ac-aaa9ec1db2e2 is destructive: effective fields are resolved from the ancestor chain at read time, so stored values can lose the definitions that give them meaning",
			key:     "eo_destructive_parent_type",
			args:    map[string]string{"from": "f7f335ae-e891-4ccc-b484-f3c2519b4a8e", "to": "f8ab0016-e375-417c-b9ac-aaa9ec1db2e2"},
		},
		{
			name: "key on a concrete type", change: EODestructiveKey,
			edit: func(b, a *EORecordType, fa []EOFieldDef) []EOFieldDef {
				a.Key = "directive_v2"
				return fa
			},
			message: `changing key "directive" to "directive_v2" is destructive: the key identifies this type to the reference prefix it owns, to every formula that names it, and to its three per-type permission codes — saving writes the codes for "directive_v2" and leaves "directive"'s codes and every role grant on them behind, so a role holding them loses this register and renaming back is refused outright`,
			key:     "eo_destructive_key",
			args:    map[string]string{"from": "directive", "to": "directive_v2"},
		},
		{
			name: "key on an abstract type", change: EODestructiveKey,
			edit: func(b, a *EORecordType, fa []EOFieldDef) []EOFieldDef {
				b.IsAbstract, a.IsAbstract = true, true
				a.Key = "directive_v2"
				return fa
			},
			message: `changing key "directive" to "directive_v2" is destructive: the key identifies this type to the reference prefix it owns, to every formula that names it`,
			key:     "eo_destructive_key_abstract",
			args:    map[string]string{"from": "directive", "to": "directive_v2"},
		},
		{
			name: "abstract", change: EODestructiveAbstract,
			edit: func(b, a *EORecordType, fa []EOFieldDef) []EOFieldDef {
				a.IsAbstract = true
				return fa
			},
			message: `making record type "directive" abstract is destructive: an abstract type holds no records of its own, and this one already has some`,
			key:     "eo_destructive_abstract",
			args:    map[string]string{"type": "directive"},
		},
		{
			name: "reference_prefix", change: EODestructiveReferencePrefix,
			edit: func(b, a *EORecordType, fa []EOFieldDef) []EOFieldDef {
				a.ReferencePrefix = "EO-DX"
				return fa
			},
			message: `changing reference prefix "EO-DIR" to "EO-DX" is destructive: existing references were issued under the old prefix`,
			key:     "eo_destructive_reference_prefix",
			args:    map[string]string{"from": "EO-DIR", "to": "EO-DX"},
		},
		{
			name: "reference_pattern", change: EODestructiveReferencePattern,
			edit: func(b, a *EORecordType, fa []EOFieldDef) []EOFieldDef {
				b.ReferencePattern, a.ReferencePattern = "EO-DIR-YYYY-NNN", "EO-DIR-YYYY-NNNN"
				return fa
			},
			message: `changing reference pattern "EO-DIR-YYYY-NNN" to "EO-DIR-YYYY-NNNN" is destructive: the sequence counter is keyed on the pattern, so a new one restarts at 1 and re-issues references already in use`,
			key:     "eo_destructive_reference_pattern",
			args:    map[string]string{"from": "EO-DIR-YYYY-NNN", "to": "EO-DIR-YYYY-NNNN"},
		},
		{
			name: "reference_mode", change: EODestructiveReferenceMode,
			edit: func(b, a *EORecordType, fa []EOFieldDef) []EOFieldDef {
				b.ReferenceMode, a.ReferenceMode = EOReferenceModeOwn, EOReferenceModeParent
				return fa
			},
			message: `changing reference mode "own" to "parent" is destructive: existing references were issued under the old mode, and the new one derives or numbers them differently`,
			key:     "eo_destructive_reference_mode",
			args:    map[string]string{"from": "own", "to": "parent"},
		},
		{
			name: "field_removed", change: EODestructiveFieldRemoved, fieldKey: "notes",
			edit:    func(_, _ *EORecordType, fa []EOFieldDef) []EOFieldDef { return fa[:1] },
			message: `removing field "notes" is destructive while records exist`,
			key:     "eo_destructive_field_removed",
			args:    map[string]string{"field": "notes"},
		},
		{
			name: "field_kind", change: EODestructiveFieldKind, fieldKey: "notes",
			edit: func(_, _ *EORecordType, fa []EOFieldDef) []EOFieldDef {
				fa[1].Kind = EOKindText
				return fa
			},
			message: `changing kind of field "notes" from "long_text" to "text" is destructive while records exist`,
			key:     "eo_destructive_field_kind",
			args:    map[string]string{"field": "notes", "from": "long_text", "to": "text"},
		},
		{
			name: "field_required", change: EODestructiveFieldRequired, fieldKey: "notes",
			edit: func(_, _ *EORecordType, fa []EOFieldDef) []EOFieldDef {
				fa[1].RequiredMode = EORequiredAtState
				return fa
			},
			message: `making field "notes" required is destructive while records exist`,
			key:     "eo_destructive_field_required",
			args:    map[string]string{"field": "notes"},
		},
	}
}

func eoRefusalFor(t *testing.T, tc eoRefusalCase, confirm *EODestructiveConfirmation) (error, *EODestructiveRefusal) {
	t.Helper()
	before, fb := eoBase()
	after := before
	fa := append([]EOFieldDef{}, fb...)
	fa = tc.edit(&before, &after, fa)
	err := ValidateTypeEdit(before, after, fb, fa, true, confirm)
	var ref *EODestructiveRefusal
	if !errors.As(err, &ref) {
		return err, nil
	}
	return err, ref
}

// TestDestructiveRefusal_MessageUnchanged pins every sentence. Plan 4's
// frontend gates the refusal banner's records claim on a word in it, and three
// reviews verified that by driving it: a later edit to the wording that is not
// also an edit to this table is a regression, not a cleanup.
func TestDestructiveRefusal_MessageUnchanged(t *testing.T) {
	for _, tc := range eoRefusalCases() {
		t.Run(tc.name, func(t *testing.T) {
			err, _ := eoRefusalFor(t, tc, nil)
			if err == nil {
				t.Fatal("not refused")
			}
			if err.Error() != tc.message {
				t.Errorf("message drifted\n got: %q\nwant: %q", err.Error(), tc.message)
			}
		})
	}
}

func TestDestructiveRefusal_CarriesKeyAndArgs(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range eoRefusalCases() {
		seen[tc.change] = true
		t.Run(tc.name, func(t *testing.T) {
			_, ref := eoRefusalFor(t, tc, nil)
			if ref == nil {
				t.Fatal("refusal is not an *EODestructiveRefusal")
			}
			if ref.Key == "" {
				t.Fatal("empty message_key")
			}
			if ref.Key != tc.key {
				t.Errorf("key = %q, want %q", ref.Key, tc.key)
			}
			if !reflect.DeepEqual(ref.Args, tc.args) {
				t.Errorf("args = %v, want %v", ref.Args, tc.args)
			}
			if ref.DetailKey != "" {
				t.Errorf("an unconfirmed refusal has no detail, got %q", ref.DetailKey)
			}
		})
	}
	for _, change := range EODestructiveChanges {
		if !seen[change] {
			t.Errorf("destructive change %q has no key/args case", change)
		}
	}
}

// Keys must be distinct per change: two changes sharing a key would render one
// as the other.
func TestDestructiveRefusal_KeysAreDistinct(t *testing.T) {
	byKey := map[string]string{}
	for _, tc := range eoRefusalCases() {
		if prev, ok := byKey[tc.key]; ok && prev != tc.change {
			t.Errorf("key %q is used by %q and %q", tc.key, prev, tc.change)
		}
		byKey[tc.key] = tc.change
	}
}

func TestDestructiveRefusal_ConfirmationMismatchDetail(t *testing.T) {
	var field eoRefusalCase
	for _, tc := range eoRefusalCases() {
		if tc.name == "field_kind" {
			field = tc
		}
	}
	reason := "backfilled by the registry clerk before the next transition"
	cases := []struct {
		name    string
		confirm EODestructiveConfirmation
		message string
		key     string
		args    map[string]string
	}{
		{
			"wrong type", EODestructiveConfirmation{TypeKey: "other", FieldKey: "notes", Change: EODestructiveFieldKind, Reason: reason},
			`changing kind of field "notes" from "long_text" to "text" is destructive while records exist. The confirmation supplied authorises a change to record type "other", and this edit changes record type "directive", so it does not authorise it`,
			"eo_destructive_confirmation_wrong_type",
			map[string]string{"confirmed_type": "other", "edit_type": "directive"},
		},
		{
			"wrong field", EODestructiveConfirmation{TypeKey: "directive", FieldKey: "title", Change: EODestructiveFieldKind, Reason: reason},
			`changing kind of field "notes" from "long_text" to "text" is destructive while records exist. The confirmation supplied authorises a change to field "title", and this edit changes field "notes", so it does not authorise it`,
			"eo_destructive_confirmation_wrong_field",
			map[string]string{"confirmed_field": "title", "edit_field": "notes"},
		},
		{
			"wrong change", EODestructiveConfirmation{TypeKey: "directive", FieldKey: "notes", Change: EODestructiveFieldRemoved, Reason: reason},
			`changing kind of field "notes" from "long_text" to "text" is destructive while records exist. The confirmation supplied authorises "field_removed", and this edit is "field_kind", so it does not authorise it`,
			"eo_destructive_confirmation_wrong_change",
			map[string]string{"confirmed_change": "field_removed", "edit_change": "field_kind"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			confirm := c.confirm
			err, ref := eoRefusalFor(t, field, &confirm)
			if ref == nil {
				t.Fatalf("not a refusal: %v", err)
			}
			if err.Error() != c.message {
				t.Errorf("message drifted\n got: %q\nwant: %q", err.Error(), c.message)
			}
			if ref.Key != "eo_destructive_field_kind" || !reflect.DeepEqual(ref.Args, field.args) {
				t.Errorf("the base refusal must still be translatable: key %q args %v", ref.Key, ref.Args)
			}
			if ref.DetailKey != c.key || !reflect.DeepEqual(ref.DetailArgs, c.args) {
				t.Errorf("detail = %q %v, want %q %v", ref.DetailKey, ref.DetailArgs, c.key, c.args)
			}
		})
	}
}

func TestDestructiveRefusal_MultipleChangesDetail(t *testing.T) {
	before, fb := eoBase()
	after := before
	after.ReferencePrefix = "EO-DX"
	after.ReferencePattern = "EO-DX-NNN"
	confirm := &EODestructiveConfirmation{TypeKey: "directive", Change: EODestructiveReferencePrefix, Reason: "backfill"}
	err := ValidateTypeEdit(before, after, fb, fb, true, confirm)
	var ref *EODestructiveRefusal
	if !errors.As(err, &ref) {
		t.Fatalf("not a refusal: %v", err)
	}
	want := `changing reference prefix "EO-DIR" to "EO-DX" is destructive: existing references were issued under the old prefix. This edit makes 2 destructive changes (reference_prefix, reference_pattern) and a confirmation authorises one: make them one at a time, each with its own confirmation`
	if err.Error() != want {
		t.Errorf("message drifted\n got: %q\nwant: %q", err.Error(), want)
	}
	wantArgs := map[string]string{"count": "2", "changes": "reference_prefix, reference_pattern"}
	if ref.Key != "eo_destructive_reference_prefix" || ref.DetailKey != "eo_destructive_multiple" || !reflect.DeepEqual(ref.DetailArgs, wantArgs) {
		t.Errorf("key %q detail %q %v", ref.Key, ref.DetailKey, ref.DetailArgs)
	}
}
