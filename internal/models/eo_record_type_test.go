package models

import (
	"testing"

	"github.com/google/uuid"
)

func TestValidateReferencePrefixRejectsDuplicate(t *testing.T) {
	existing := []EORecordType{{Key: "directive", ReferencePrefix: "EO-DIR"}}
	if err := ValidateReferencePrefix("EO-DIR", existing); err == nil {
		t.Error("duplicate prefix accepted; reference uniqueness is per-partition only")
	}
	if err := ValidateReferencePrefix("eo-dir", existing); err == nil {
		t.Error("case-variant duplicate accepted")
	}
	if err := ValidateReferencePrefix("EO-COM", existing); err != nil {
		t.Errorf("unique prefix rejected: %v", err)
	}
}

func TestValidateReferencePrefixRejectsPrefixOfAnother(t *testing.T) {
	existing := []EORecordType{{Key: "directive", ReferencePrefix: "EO-DIR"}}
	if err := ValidateReferencePrefix("EO", existing); err == nil {
		t.Error("prefix that is a prefix of an existing one accepted")
	}
	if err := ValidateReferencePrefix("EO-DIR-X", existing); err == nil {
		t.Error("prefix that extends an existing one accepted")
	}
	if err := ValidateReferencePrefix("eo", existing); err == nil {
		t.Error("lowercase prefix-of-another accepted")
	}
}

func TestValidateReferencePrefixRejectsEmpty(t *testing.T) {
	if err := ValidateReferencePrefix("  ", nil); err == nil {
		t.Error("blank prefix accepted")
	}
}

func TestValidateReferencePrefixIgnoresTypesWithoutPrefix(t *testing.T) {
	// Abstract bases carry no prefix; an empty existing prefix must not
	// count as a prefix of everything.
	existing := []EORecordType{{Key: "base", IsAbstract: true}}
	if err := ValidateReferencePrefix("EO-DIR", existing); err != nil {
		t.Errorf("rejected against a type with no prefix: %v", err)
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
