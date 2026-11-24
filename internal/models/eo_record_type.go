package models

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Field kinds (spec §7.3). `table` is deliberately not a kind: execution-plan
// stages are a child record type instead. `org unit` and `external body` both
// resolve to departments; the internal/external distinction lives in Config.
const (
	EOKindAutoNumber      = "auto_number"
	EOKindText            = "text"
	EOKindLongText        = "long_text"
	EOKindDropdown        = "dropdown"
	EOKindMultiSelect     = "multi_select"
	EOKindNumber          = "number"
	EOKindPercent         = "percent"
	EOKindYesNo           = "yes_no"
	EOKindDate            = "date"
	EOKindDateTime        = "date_time"
	EOKindTime            = "time"
	EOKindFile            = "file"
	EOKindMultiFile       = "multi_file"
	EOKindUser            = "user"
	EOKindMultiUser       = "multi_user"
	EOKindUserDate        = "user_date" // approval/signature primitive: one kind, not two fields
	EOKindOrgUnit         = "org_unit"
	EOKindMultiOrgUnit    = "multi_org_unit"
	EOKindExternalBody    = "external_body"
	EOKindEmail           = "email"
	EOKindPhone           = "phone"
	EOKindURL             = "url"
	EOKindLink            = "link"
	EOKindMultiLink       = "multi_link"
	EOKindWorkflowState   = "workflow_state"
	EOKindMultiUserStatus = "multi_user_status" // present | absent | excused per user
	EOKindMultiUserAck    = "multi_user_ack"    // agreed | reservation (+ reservation text) per user
)

// EOFieldKinds is the allow-list of field kinds, built from the constants
// above rather than from a second hand-written list of strings: renaming a
// kind's value changes this list with it, because each entry IS the constant.
//
// The remaining drift — declaring a new EOKind* constant and forgetting to add
// it here — is what TestEOFieldKindsCoversEveryKindConstant catches: it parses
// this file and fails if any EOKind* constant is missing from the slice.
//
// The designer validates `kind` against this (handlers.eoValidateFieldShape).
// It validates the NAME of the kind only. Per-kind validation of a stored
// VALUE at save time — that a `yes_no` holds a boolean, that a `dropdown`
// holds one of its configured options — is deliberately not here: that is Plan
// 2's work, alongside the forms that render each kind.
var EOFieldKinds = []string{
	EOKindAutoNumber,
	EOKindText,
	EOKindLongText,
	EOKindDropdown,
	EOKindMultiSelect,
	EOKindNumber,
	EOKindPercent,
	EOKindYesNo,
	EOKindDate,
	EOKindDateTime,
	EOKindTime,
	EOKindFile,
	EOKindMultiFile,
	EOKindUser,
	EOKindMultiUser,
	EOKindUserDate,
	EOKindOrgUnit,
	EOKindMultiOrgUnit,
	EOKindExternalBody,
	EOKindEmail,
	EOKindPhone,
	EOKindURL,
	EOKindLink,
	EOKindMultiLink,
	EOKindWorkflowState,
	EOKindMultiUserStatus,
	EOKindMultiUserAck,
}

// eoFieldKindSet is EOFieldKinds as a set, built once.
var eoFieldKindSet = func() map[string]bool {
	out := make(map[string]bool, len(EOFieldKinds))
	for _, k := range EOFieldKinds {
		out[k] = true
	}
	return out
}()

// IsEOFieldKind reports whether kind is one of the configured field kinds.
func IsEOFieldKind(kind string) bool { return eoFieldKindSet[kind] }

// Provenance: who or what sets a field's value (spec §7.4).
const (
	EOProvenanceUser    = "user"
	EOProvenanceAuto    = "auto"
	EOProvenanceDerived = "derived"
	EOProvenanceSystem  = "system"
)

// RequiredMode: when a field must have a value (spec §7.4).
const (
	EORequiredNever       = "never"
	EORequiredAlways      = "always"
	EORequiredConditional = "conditional"
	EORequiredAtState     = "at_state"
)

// ReferenceMode: whether a type numbers its own references or uses its parent's (spec §7.9).
const (
	EOReferenceModeOwn    = "own"
	EOReferenceModeParent = "parent"
)

// Link cardinality: how many targets a source record may link to via a link type.
const (
	EOCardinalityOne  = "one"
	EOCardinalityMany = "many"
)

// EORecordType describes a configurable record type.
type EORecordType struct {
	ID              uuid.UUID  `gorm:"type:uuid;primary_key" json:"id"`
	Key             string     `gorm:"size:100;not null;uniqueIndex:idx_eo_record_types_key,where:deleted_at IS NULL" json:"key"`
	Name            string     `gorm:"size:255;not null" json:"name"`
	NameAr          string     `gorm:"size:255" json:"name_ar"`
	Icon            string     `gorm:"size:100" json:"icon"`
	ParentTypeID    *uuid.UUID `gorm:"type:uuid;index" json:"parent_type_id"`
	IsAbstract      bool       `gorm:"default:false" json:"is_abstract"`
	ReferencePrefix string     `gorm:"size:50" json:"reference_prefix"`
	ReferenceMode   string     `gorm:"size:20;default:'own'" json:"reference_mode"`
	// ReferencePattern is the type's numbering pattern, in the grammar
	// services.RenderReference parses: an own-sequence pattern such as
	// EO-DIR-YYYY-NNN, or a parent-derived one such as EO-DIR-YYYY-NNN-A#
	// (spec §7.9). ReferencePrefix states which prefixes a type owns, and
	// ValidateReferencePrefix keeps those from overlapping; the pattern says
	// how a reference is rendered, which the prefix alone cannot (it carries
	// neither the sequence width nor a child's suffix letter). A type cannot
	// be numbered without one.
	ReferencePattern string         `gorm:"size:100" json:"reference_pattern"`
	WorkflowID       *uuid.UUID     `gorm:"type:uuid;index" json:"workflow_id"`
	Capabilities     datatypes.JSON `gorm:"type:jsonb;default:'[]'::jsonb" json:"capabilities"`
	IsActive         bool           `gorm:"default:true" json:"is_active"`
	// PartitionPending records that this type has no partition of its own and
	// its records live in eo_records_default (spec §7.7). In Plan 1 NOTHING
	// EVER CLEARS IT: no per-type partition is created, by the designer or by
	// any command, so every record of every type is in the default partition
	// and the platform is fully functional there. It is a LABEL — the designer
	// displays it — and nothing in the engine reads it.
	//
	// It is written true on every type the designer creates, and the column
	// defaults to true. The seed writes it FALSE for the abstract base type
	// eo_base, which holds no records and will never want a partition
	// (seed_eo_platform.go). So "always true" would be wrong as a claim about
	// the data, even though nothing ever flips a true to a false.
	//
	// Promotion is a ONE-WAY DOOR and it closes at the type's first record.
	// Creating a partition of a table that already has a DEFAULT partition
	// makes Postgres take an ACCESS EXCLUSIVE lock on that default and
	// FULL-SCAN it to prove no existing row would belong to the new partition.
	// That scan happens whether or not the type being promoted has records, and
	// here the default holds every record of every type — so promoting an EMPTY
	// type is the cheap case only RELATIVE to the other: a plain CREATE TABLE …
	// PARTITION OF, still scanning the whole register under lock. Promoting a
	// POPULATED one adds a data move on top (create detached, copy, delete,
	// attach), with the lock held across it. Neither is free on a populated
	// register. Plan 2 must therefore create each type's partition BEFORE that
	// type's first record, at type creation rather than at the next release.
	// Nothing here enforces that ordering. Recorded in spec §7.7 during the
	// Plan 1 fix wave.
	PartitionPending bool           `gorm:"default:true" json:"partition_pending"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"-"`

	ParentType *EORecordType `gorm:"foreignKey:ParentTypeID" json:"parent_type,omitempty"`
	Workflow   *Workflow     `gorm:"foreignKey:WorkflowID" json:"workflow,omitempty"`
	Fields     []EOFieldDef  `gorm:"foreignKey:RecordTypeID" json:"fields,omitempty"`
}

func (EORecordType) TableName() string { return "eo_record_types" }

func (x *EORecordType) BeforeCreate(tx *gorm.DB) error {
	if x.ID == uuid.Nil {
		x.ID = uuid.New()
	}
	return nil
}

// EOFieldDef describes one field of a record type.
type EOFieldDef struct {
	ID                   uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	RecordTypeID         uuid.UUID      `gorm:"type:uuid;not null;uniqueIndex:idx_eo_field_defs_type_key,where:deleted_at IS NULL" json:"record_type_id"`
	Key                  string         `gorm:"size:100;not null;uniqueIndex:idx_eo_field_defs_type_key,where:deleted_at IS NULL" json:"key"`
	Label                string         `gorm:"size:255;not null" json:"label"`
	LabelAr              string         `gorm:"size:255" json:"label_ar"`
	Kind                 string         `gorm:"size:50;not null" json:"kind"`
	Config               datatypes.JSON `gorm:"type:jsonb;default:'{}'::jsonb" json:"config"`
	Provenance           string         `gorm:"size:20;default:'user'" json:"provenance"`
	RequiredMode         string         `gorm:"size:20;default:'never'" json:"required_mode"`
	RequiredRule         string         `gorm:"type:text" json:"required_rule"`
	RequiredAtStateIDs   datatypes.JSON `gorm:"type:jsonb;default:'[]'::jsonb" json:"required_at_state_ids"`
	Formula              string         `gorm:"type:text" json:"formula"`
	RecomputeOn          datatypes.JSON `gorm:"type:jsonb;default:'[]'::jsonb" json:"recompute_on"`
	IsIndexed            bool           `gorm:"default:false" json:"is_indexed"`
	IsLockedAfterStateID *uuid.UUID     `gorm:"type:uuid" json:"is_locked_after_state_id"`
	SortOrder            int            `gorm:"default:0" json:"sort_order"`
	HelpText             string         `gorm:"size:1000" json:"help_text"`
	HelpTextAr           string         `gorm:"size:1000" json:"help_text_ar"`
	// ArUntranslated marks a field whose Arabic wording has not been signed
	// off by the client: either a placeholder (the English text standing in
	// for Arabic) or wording an implementer wrote. It is a column of its own
	// so that it survives an administrator editing ANY other attribute of
	// the field: the designer's update path loads the stored row, applies the
	// request's attributes onto it and saves the whole struct, and this
	// column is not one the request carries. Nothing clears it automatically;
	// see the pilot seed (internal/database/seed_eo_pilot.go).
	//
	// WRITE-ONLY ON THE DESIGNER PATH, BY DESIGN. It must NEVER be added to
	// the designer's eoFieldDefRequest: that is exactly what would let any
	// designer save clear it silently
	// (handlers.TestFieldDefRequestCannotCarryTheUntranslatedMarker pins the
	// absence). An administrator retyping label_ar is not client sign-off, so
	// editing it does not clear the flag.
	//
	// THE ONE THING THAT CLEARS IT is POST /api/v1/admin/eo-signoff/discharge
	// (handlers.EOSignoffHandler), which clears the marker on the field
	// definitions whose ids it is GIVEN — never all of them — and records who
	// did it in the action log, in the same transaction. That route, with GET
	// /api/v1/admin/eo-signoff which lists what is still outstanding, is the
	// deliberate operation "Arabic sign-off clearing" in
	// docs/superpowers/eo-plan1-carry-forward.md asked for.
	ArUntranslated bool           `gorm:"not null;default:false;index" json:"ar_untranslated"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`

	RecordType *EORecordType `gorm:"foreignKey:RecordTypeID" json:"-"`
}

func (EOFieldDef) TableName() string { return "eo_field_defs" }

func (x *EOFieldDef) BeforeCreate(tx *gorm.DB) error {
	if x.ID == uuid.Nil {
		x.ID = uuid.New()
	}
	return nil
}

// EOLinkType describes a typed relationship between records (spec §7.6).
// BlocksParentTransitionKey, when set, names the transition that is blocked
// instead of closure.
type EOLinkType struct {
	ID            uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	Key           string         `gorm:"size:100;not null;uniqueIndex:idx_eo_link_types_key,where:deleted_at IS NULL" json:"key"`
	Label         string         `gorm:"size:255;not null" json:"label"`
	LabelAr       string         `gorm:"size:255" json:"label_ar"`
	SourceTypeIDs datatypes.JSON `gorm:"type:jsonb;default:'[]'::jsonb" json:"source_type_ids"`
	TargetTypeIDs datatypes.JSON `gorm:"type:jsonb;default:'[]'::jsonb" json:"target_type_ids"`
	// Cardinality is EOCardinalityOne or EOCardinalityMany.
	Cardinality               string         `gorm:"size:50" json:"cardinality"`
	DeriveDeadlineFromParent  bool           `gorm:"default:false" json:"derive_deadline_from_parent"`
	DeadlineOffsetDays        int            `gorm:"default:0" json:"deadline_offset_days"`
	BlocksParentClosure       bool           `gorm:"default:false" json:"blocks_parent_closure"`
	BlocksParentTransitionKey string         `gorm:"size:100" json:"blocks_parent_transition_key"`
	CascadeSensitivity        bool           `gorm:"default:false" json:"cascade_sensitivity"`
	CreatedAt                 time.Time      `json:"created_at"`
	UpdatedAt                 time.Time      `json:"updated_at"`
	DeletedAt                 gorm.DeletedAt `gorm:"index" json:"-"`
}

func (EOLinkType) TableName() string { return "eo_link_types" }

func (x *EOLinkType) BeforeCreate(tx *gorm.DB) error {
	if x.ID == uuid.Nil {
		x.ID = uuid.New()
	}
	return nil
}

// ValidateReferencePrefix rejects a prefix that is blank, or that equals or is
// a prefix of (or is prefixed by) an existing type's prefix, case-insensitively.
// reference is unique only within a partition, so global uniqueness rests
// entirely on prefixes never overlapping. Types without a prefix are ignored.
//
// It takes the whole type, not just the string, because TWO kinds of type own
// no prefix at all and the rule must not be asked of them:
//
//   - an ABSTRACT type holds no records of its own, so it issues no references;
//   - a PARENT-DERIVED type (ReferenceMode "parent") issues every reference
//     inside its parent's — EO-SMOKE-2026-001-T1 under EO-SMOKE-2026-001 — so
//     a truthful prefix for it ALWAYS overlaps its parent's and a blank one
//     would be rejected outright. Global uniqueness still holds for it without
//     a prefix of its own: a child reference is its parent's plus a suffix the
//     parent's own sequence can never produce, and the parent's prefix is
//     already guarded here. Without this exemption every parent-derived type
//     has to carry a prefix that is a small lie about the references it issues
//     — and ValidateTypeEdit then holds that lie in place, because changing a
//     prefix is a destructive edit once records exist, correctable after that
//     only as a §7.14 confirmed destructive edit with a stated backfill rule.
//
// On an update, the caller MUST exclude the type being edited from existing,
// otherwise a type that keeps its own prefix is rejected against itself.
// MESSAGES ARE ENGLISH-ONLY, against spec §5's bilingual requirement. The
// error returned here is passed to the client verbatim by the handler, so a
// refusal reaches an Arabic UI in English. Noted, not fixed, in the Plan 1 fix
// wave (2026-11-22): the fix is to return an i18n KEY plus its arguments
// instead of a sentence, and it belongs with Plan 2's forms, which is where
// the client-signed Arabic labels for each field and kind arrive. Writing
// Arabic wording the client has not approved now would only have to be
// revised then. See spec §5.
func ValidateReferencePrefix(t EORecordType, existing []EORecordType) error {
	if t.IsAbstract || t.ReferenceMode == EOReferenceModeParent {
		return nil
	}
	p := strings.ToUpper(strings.TrimSpace(t.ReferencePrefix))
	if p == "" {
		return fmt.Errorf("reference prefix is required")
	}
	for _, e := range existing {
		o := strings.ToUpper(strings.TrimSpace(e.ReferencePrefix))
		if o == "" {
			continue
		}
		if strings.HasPrefix(p, o) || strings.HasPrefix(o, p) {
			return fmt.Errorf("reference prefix %q overlaps %q used by record type %q", t.ReferencePrefix, e.ReferencePrefix, e.Key)
		}
	}
	return nil
}

// ── §7.14's confirmed destructive edit ──────────────────────────────────────

// The destructive changes ValidateTypeEdit refuses while records exist, one
// identifier each. A confirmation names ONE of them, on one field, of one
// type. Naming the type and the field is not enough on its own: an operator
// shown "this removes field X" was asked about removing it and not about
// re-kinding it, and a confirmation that did not say which would carry
// whichever change the request happened to contain.
//
// THESE STRINGS ARE A WIRE CONTRACT. They arrive in the request body as
// `confirm_destructive.change` and are stored verbatim in the audit row, so
// renaming one invalidates confirmations a client already knows how to send
// and makes the rows already stored unreadable against the new set.
const (
	EODestructiveWorkflow         = "workflow"
	EODestructiveParentType       = "parent_type"
	EODestructiveKey              = "key"
	EODestructiveAbstract         = "abstract"
	EODestructiveReferencePrefix  = "reference_prefix"
	EODestructiveReferencePattern = "reference_pattern"
	EODestructiveReferenceMode    = "reference_mode"
	EODestructiveFieldRemoved     = "field_removed"
	EODestructiveFieldKind        = "field_kind"
	EODestructiveFieldRequired    = "field_required"
)

// EODestructiveChanges is every identifier a confirmation may name. It is the
// list a client offers and the list a refusal quotes back, and it exists so
// that neither is a second copy of the constants above.
var EODestructiveChanges = []string{
	EODestructiveWorkflow,
	EODestructiveParentType,
	EODestructiveKey,
	EODestructiveAbstract,
	EODestructiveReferencePrefix,
	EODestructiveReferencePattern,
	EODestructiveReferenceMode,
	EODestructiveFieldRemoved,
	EODestructiveFieldKind,
	EODestructiveFieldRequired,
}

// EODestructiveConfirmation is spec §7.14's second half: a destructive change
// is "blocked while records of that type exist, OR applied with explicit
// confirmation and a stated backfill rule. Never silently."
//
// It names the edit it authorises rather than recording that somebody clicked
// yes. A confirmation that named nothing would be a blanket override with
// extra steps, and it would be worse than that here: ValidateTypeEdit's caller
// computes hasRecords over the type AND EVERY TYPE BENEATH IT, so an edit to
// an abstract ancestor is guarded by its descendants' records — and a
// confirmation obtained while looking at a child register would authorise a
// change to the parent that nobody reviewed. TypeKey is what stops that.
//
// Reason is the stated backfill rule, in the operator's own words. THERE IS
// NO EO BACKFILL in this repository — nothing reads this rule and nothing acts
// on it. It is a statement of what a human will do about the records this edit
// leaves behind, and its whole value is that it is recorded next to the change
// and attributed. It is required.
//
// FieldKey is empty for the six changes that are not about one field, and
// that emptiness is compared like any other value: a confirmation that names
// a field does not authorise a change to the type itself, and one that names
// none does not authorise a change to a field.
type EODestructiveConfirmation struct {
	TypeKey  string `json:"type_key"`
	FieldKey string `json:"field_key"`
	Change   string `json:"change"`
	Reason   string `json:"reason"`
}

// eoMaxConfirmationReason bounds the stated rule. It is a sentence an operator
// types into a dialog, and it is rendered into models.ActionLog.Description,
// which is varchar(500): an unbounded one would be truncated there anyway, and
// a value too long for the column would fail the audit INSERT and — because
// that INSERT shares the edit's transaction — roll the edit back with an
// internal error instead of refusing the request.
const eoMaxConfirmationReason = 1000

// validate checks the confirmation is well formed, before anything is compared
// against the edit. A malformed one is refused rather than ignored, even on an
// edit with nothing to confirm: an unread confirmation in a request body is
// indistinguishable, to the operator who typed it, from one that was honoured.
func (cf EODestructiveConfirmation) validate() error {
	if strings.TrimSpace(cf.TypeKey) == "" {
		return fmt.Errorf("a destructive-edit confirmation must name the record type it authorises (type_key)")
	}
	if strings.TrimSpace(cf.Change) == "" {
		return fmt.Errorf("a destructive-edit confirmation must name the change it authorises (change), one of: %s", strings.Join(EODestructiveChanges, ", "))
	}
	known := false
	for _, change := range EODestructiveChanges {
		if cf.Change == change {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("a destructive-edit confirmation names change %q, which is not one of: %s", cf.Change, strings.Join(EODestructiveChanges, ", "))
	}
	if strings.TrimSpace(cf.Reason) == "" {
		return fmt.Errorf("a destructive-edit confirmation must state the backfill rule (reason): what will be done about the records this change leaves behind")
	}
	// COUNTED IN CHARACTERS, NOT BYTES. The reason is typed in Arabic as
	// often as in English, every letter of which is two bytes in UTF-8, so a
	// byte count would refuse a 500-character Arabic rule while allowing a
	// 1000-character English one — and would say "1000 characters" about a
	// sentence half that long. Postgres varchar(n) counts characters too.
	if utf8.RuneCountInString(cf.Reason) > eoMaxConfirmationReason {
		return fmt.Errorf("a destructive-edit confirmation's stated backfill rule is %d characters; the limit is %d", utf8.RuneCountInString(cf.Reason), eoMaxConfirmationReason)
	}
	return nil
}

// eoDestructiveEdit is one destructive change a proposed edit makes: what it
// is, where it is, and the sentence the operator is shown when there is no
// confirmation for it.
type eoDestructiveEdit struct {
	Change   string
	TypeKey  string
	FieldKey string
	Refusal  string
}

// authorises returns nil when cf names this exact change, and otherwise the
// refusal plus what the confirmation names instead. Keys are compared
// trimmed because the designer trims them before storing them, so an
// untrimmed confirmation names the same field the stored definition does.
func (cf EODestructiveConfirmation) authorises(e eoDestructiveEdit) error {
	if strings.TrimSpace(cf.TypeKey) != e.TypeKey {
		return fmt.Errorf("%s. The confirmation supplied authorises a change to record type %q, and this edit changes record type %q, so it does not authorise it",
			e.Refusal, strings.TrimSpace(cf.TypeKey), e.TypeKey)
	}
	if strings.TrimSpace(cf.FieldKey) != e.FieldKey {
		return fmt.Errorf("%s. The confirmation supplied authorises a change to %s, and this edit changes %s, so it does not authorise it",
			e.Refusal, eoConfirmationFieldText(strings.TrimSpace(cf.FieldKey)), eoConfirmationFieldText(e.FieldKey))
	}
	if cf.Change != e.Change {
		return fmt.Errorf("%s. The confirmation supplied authorises %q, and this edit is %q, so it does not authorise it",
			e.Refusal, cf.Change, e.Change)
	}
	return nil
}

// eoConfirmationFieldText renders the "where" half of a mismatch, so that the
// type-level case reads as a sentence rather than as an empty pair of quotes.
func eoConfirmationFieldText(key string) string {
	if key == "" {
		return "the record type itself rather than one of its fields"
	}
	return fmt.Sprintf("field %q", key)
}

// ValidateTypeEdit applies the guard rails of spec §7.14. Additive changes are
// always allowed. While records exist, removing a field, changing its kind,
// making an existing field required, changing the reference prefix or pattern,
// re-keying the type, repointing its workflow or its parent, or turning it
// abstract is rejected — UNLESS confirm names that exact change and states a
// backfill rule, which is §7.14's other half. It never deletes anything: a
// removed field's data stays in the values jsonb so a mistaken removal is
// recoverable. Fields are matched by Key.
//
// A NIL CONFIRMATION IS TODAY'S BEHAVIOUR, down to the wording of the refusal:
// the first destructive change found is returned, in the order the rules are
// listed above. Nothing about an unconfirmed edit changed when the confirmed
// path was added.
//
// ONE CONFIRMATION AUTHORISES ONE CHANGE. An edit that makes two destructive
// changes is refused even when the confirmation names one of them, because the
// other would then be applied under a confirmation nobody gave for it. Both
// are reachable in one request — a PUT replaces the whole attribute set, so
// the prefix and the pattern, or a field's kind and its requiredness, change
// together — and the remedy is to make them one at a time, each confirmed.
//
// Every rule here is conditional on hasRecords. None of them is "never": a
// type nobody has used yet is still being designed and may be changed freely,
// and on such a type a confirmation authorises nothing — it is still checked
// for being well formed, because a confirmation that is read by nothing and
// reported as nothing is indistinguishable, to the operator who typed it,
// from one that was honoured.
// MESSAGES ARE ENGLISH-ONLY, against spec §5's bilingual requirement. The
// error returned here is passed to the client verbatim by the handler, so a
// refusal reaches an Arabic UI in English. Noted, not fixed, in the Plan 1 fix
// wave (2026-11-22): the fix is to return an i18n KEY plus its arguments
// instead of a sentence, and it belongs with Plan 2's forms, which is where
// the client-signed Arabic labels for each field and kind arrive. Writing
// Arabic wording the client has not approved now would only have to be
// revised then. See spec §5.
func ValidateTypeEdit(before, after EORecordType, fieldsBefore, fieldsAfter []EOFieldDef, hasRecords bool, confirm *EODestructiveConfirmation) error {
	if confirm != nil {
		if err := confirm.validate(); err != nil {
			return err
		}
	}
	if !hasRecords {
		return nil
	}
	changes := eoDestructiveChanges(before, after, fieldsBefore, fieldsAfter)
	if len(changes) == 0 {
		return nil
	}
	if confirm == nil {
		return fmt.Errorf("%s", changes[0].Refusal)
	}
	if len(changes) > 1 {
		named := make([]string, 0, len(changes))
		for _, change := range changes {
			named = append(named, change.Change)
		}
		return fmt.Errorf("%s. This edit makes %d destructive changes (%s) and a confirmation authorises one: make them one at a time, each with its own confirmation",
			changes[0].Refusal, len(changes), strings.Join(named, ", "))
	}
	return confirm.authorises(changes[0])
}

// eoDestructiveChanges lists every destructive change the edit makes, in the
// order spec §7.14's rules are stated. It assumes records exist: the caller
// applies that condition.
//
// IT IS ORDERED, AND THE ORDER IS LOAD-BEARING. Its caller asks it twice for
// the same edit — once with a confirmation and once without — and compares the
// answers, so the sequence must be the same both times. It is, because every
// rule below is a straight-line check and the field loop iterates fieldsBefore,
// a SLICE. A guard added later that ranges over a MAP would make the first
// refusal differ between two calls on identical input, and the audit row would
// then record a refusal the operator was never shown.
//
// IT COLLECTS ALL OF THEM rather than returning at the first. The first is
// still what an unconfirmed edit is refused with, so the operator-facing
// behaviour is unchanged — but a confirmation has to be checked against the
// WHOLE set, or an edit carrying a second destructive change would smuggle it
// in behind the confirmation given for the first.
func eoDestructiveChanges(before, after EORecordType, fieldsBefore, fieldsAfter []EOFieldDef) []eoDestructiveEdit {
	var out []eoDestructiveEdit
	add := func(change, fieldKey, refusal string) {
		out = append(out, eoDestructiveEdit{Change: change, TypeKey: before.Key, FieldKey: fieldKey, Refusal: refusal})
	}
	// A record's state_id names a state of the type's CURRENT workflow.
	// Repointing the workflow leaves every existing record in a state its
	// workflow does not contain: no transition runs from it, AvailableTransitions
	// returns nothing, and no API path can put the record back — in a register
	// that is a statutory audit trail. This is the worst of the five.
	if !eoSameUUID(before.WorkflowID, after.WorkflowID) {
		add(EODestructiveWorkflow, "", fmt.Sprintf("changing workflow from %s to %s is destructive: every existing record's state_id names a state of the old workflow, and no transition runs from a state the new workflow does not contain",
			eoUUIDText(before.WorkflowID), eoUUIDText(after.WorkflowID)))
	}
	// Inherited fields are resolved at read time from the ancestor chain
	// (spec §7.2), so re-parenting changes which definitions a record's
	// stored values have. Values under a key the new chain does not define
	// stop being validated, displayed or derivable — they are still in the
	// jsonb, but nothing knows what they are.
	if !eoSameUUID(before.ParentTypeID, after.ParentTypeID) {
		add(EODestructiveParentType, "", fmt.Sprintf("changing parent type from %s to %s is destructive: effective fields are resolved from the ancestor chain at read time, so stored values can lose the definitions that give them meaning",
			eoUUIDText(before.ParentTypeID), eoUUIDText(after.ParentTypeID)))
	}
	// The key identifies the type to configuration that names it by key —
	// the reference prefix it owns, formulas that address a child type by key
	// (eoRecordDeps.Children, .Ancestor), seeded capability config, and spec
	// §7.11's three per-type permission codes.
	//
	// THE PERMISSION CODES ARE IN THE MESSAGE BECAUSE THE MESSAGE IS WHAT AN
	// OPERATOR READS BEFORE DECIDING TO OVERRIDE IT. On a concrete type the
	// repository writes the codes for the NEW key on save and leaves the old
	// key's codes, and every role grant on them, behind: a custom role holding
	// `<old>:view` silently loses the register, and renaming back is then
	// refused outright because those stranded codes still occupy their names
	// (repository.ErrEOTypePermissionCodesExist). That consequence is parked
	// rather than fixed — it needs ownership metadata on the shared
	// permissions table — so until it is fixed, saying so here is the whole of
	// the protection.
	//
	// A CONFIRMATION FOR THIS CHANGE NAMES THE OLD KEY, like every other: the
	// type is identified by what it is called now, which is the only name the
	// operator and the stored rows agree on when the request arrives.
	if before.Key != after.Key {
		// THE PERMISSION CLAUSE IS OMITTED FOR AN ABSTRACT TYPE, on either
		// side of the edit, because an abstract type HAS no per-type codes:
		// the repository writes them for concrete types only. Re-keying an
		// abstract ancestor is reachable — its descendants' records are what
		// make the edit destructive — and telling that operator their grants
		// were stranded would be false.
		permissions := fmt.Sprintf(", and to its three per-type permission codes — saving writes the codes for %q and leaves %q's codes and every role grant on them behind, so a role holding them loses this register and renaming back is refused outright",
			after.Key, before.Key)
		if before.IsAbstract || after.IsAbstract {
			permissions = ""
		}
		add(EODestructiveKey, "", fmt.Sprintf("changing key %q to %q is destructive: the key identifies this type to the reference prefix it owns, to every formula that names it%s",
			before.Key, after.Key, permissions))
	}
	// An abstract type holds no records of its own: the save pipeline refuses
	// to create one. Flipping the flag on a type that already has records
	// declares that those records cannot exist.
	if !before.IsAbstract && after.IsAbstract {
		add(EODestructiveAbstract, "", fmt.Sprintf("making record type %q abstract is destructive: an abstract type holds no records of its own, and this one already has some",
			before.Key))
	}
	if before.ReferencePrefix != after.ReferencePrefix {
		add(EODestructiveReferencePrefix, "", fmt.Sprintf("changing reference prefix %q to %q is destructive: existing references were issued under the old prefix",
			before.ReferencePrefix, after.ReferencePrefix))
	}
	// The sequence scope is keyed on the pattern (services.EOSequenceScope),
	// so changing it restarts the counter at 1 under a new scope and re-issues
	// references that are already in use.
	if before.ReferencePattern != after.ReferencePattern {
		add(EODestructiveReferencePattern, "", fmt.Sprintf("changing reference pattern %q to %q is destructive: the sequence counter is keyed on the pattern, so a new one restarts at 1 and re-issues references already in use",
			before.ReferencePattern, after.ReferencePattern))
	}
	// ReferenceMode decides whether a record's reference is issued from the
	// type's own sequence or derived from its parent's reference (spec §7.9).
	// It is a different field from the prefix and the pattern, so neither of
	// those guards sees it: flipping own to parent leaves every issued
	// reference in a form the new mode would not have produced. An unset mode
	// means own (the column default), so "" to "own" is not a change.
	if eoReferenceModeOrDefault(before.ReferenceMode) != eoReferenceModeOrDefault(after.ReferenceMode) {
		add(EODestructiveReferenceMode, "", fmt.Sprintf("changing reference mode %q to %q is destructive: existing references were issued under the old mode, and the new one derives or numbers them differently",
			eoReferenceModeOrDefault(before.ReferenceMode), eoReferenceModeOrDefault(after.ReferenceMode)))
	}
	next := make(map[string]EOFieldDef, len(fieldsAfter))
	for _, f := range fieldsAfter {
		next[f.Key] = f
	}
	for _, old := range fieldsBefore {
		cur, ok := next[old.Key]
		if !ok {
			add(EODestructiveFieldRemoved, old.Key, fmt.Sprintf("removing field %q is destructive while records exist", old.Key))
			// A field that is gone has no kind to change and no requiredness
			// to tighten; the two rules below are about a field that is still
			// there under a different definition.
			continue
		}
		if cur.Kind != old.Kind {
			add(EODestructiveFieldKind, old.Key, fmt.Sprintf("changing kind of field %q from %q to %q is destructive while records exist", old.Key, old.Kind, cur.Kind))
		}
		if isNeverRequired(old.RequiredMode) && !isNeverRequired(cur.RequiredMode) {
			add(EODestructiveFieldRequired, old.Key, fmt.Sprintf("making field %q required is destructive while records exist", old.Key))
		}
	}
	return out
}

func eoReferenceModeOrDefault(mode string) string {
	if mode == "" {
		return EOReferenceModeOwn
	}
	return mode
}

func isNeverRequired(mode string) bool { return mode == "" || mode == EORequiredNever }

// eoSameUUID compares two optional ids, treating nil as its own value: nil to
// set and set to nil are both changes, and a == b on the pointers would
// compare addresses.
func eoSameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// eoUUIDText renders an optional id for an error message.
func eoUUIDText(id *uuid.UUID) string {
	if id == nil {
		return "none"
	}
	return id.String()
}
