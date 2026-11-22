package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// Capability is behaviour a record type cannot express as configuration — a
// clash check against a diary, a quorum calculation, an approval stamp (spec
// §7.9). It is a Go module registered by key, enabled per record type, and
// handed that type's config blob for the key on every call.
//
// OnSave from the spec's hook list is not here: nothing in the seven processes
// needs it yet, and Validate already runs on the save path.
//
// Every record-facing hook is handed an EOCapabilityReader. It is the ONLY way
// a capability reaches a record other than the one it was called about, and it
// is never nil: the engine binds it to the handles that are safe on the path
// the hook is running on. A capability must not hold a repository of its own —
// see EOCapabilityReader.
type Capability interface {
	Key() string
	// Derive returns engine-computed values to merge into the record.
	Derive(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) (map[string]any, error)
	// Validate returns field key -> message, in the shape the save pipeline's
	// validation errors use. The error return is for a broken configuration.
	Validate(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) (map[string]string, error)
	// OnTransition runs inside the transition's transaction, after the field
	// changes are applied and before the record is written, and may mutate rec.
	// An error aborts the transition.
	OnTransition(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, t *models.WorkflowTransition, cfg datatypes.JSON) error
	Timers(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) ([]models.EOTimer, error)
	// ReservedKeys lists the values keys this capability writes itself. A
	// request must never be able to write them, so a record type whose fields
	// or transition field changes name one is a misconfiguration the pipelines
	// refuse (a field change needs no definition to be admitted, so "do not
	// declare it" is not enough on its own).
	ReservedKeys(cfg datatypes.JSON) ([]string, error)
	// ValidateWorkflow checks cfg against the workflow it will run in, for
	// references the config cannot check alone (transition codes).
	ValidateWorkflow(cfg datatypes.JSON, wf *models.Workflow) error
}

// ErrEOUnknownCapability is returned when a record type enables a capability
// key that nothing registered. Configuration that names a capability which
// will never run must fail loudly, not appear to work.
var ErrEOUnknownCapability = errors.New("unknown capability")

// ErrEOCapabilityConfig marks a record type whose configuration conflicts with
// an enabled capability.
var ErrEOCapabilityConfig = errors.New("capability configuration conflict")

// EOEnabledCapability is a registered capability bound to the config the
// record type gave it.
type EOEnabledCapability struct {
	Capability
	Config datatypes.JSON
}

type eoCapabilityRegistry struct {
	mu   sync.RWMutex
	caps map[string]Capability
	// sealed is set when RegisterEOCapabilities returns. See seal.
	sealed bool
}

func newEOCapabilityRegistry() *eoCapabilityRegistry {
	return &eoCapabilityRegistry{caps: map[string]Capability{}}
}

func (r *eoCapabilityRegistry) register(c Capability) {
	if c == nil || c.Key() == "" {
		panic("eo: RegisterCapability: a capability needs a key")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		panic(fmt.Sprintf("eo: RegisterCapability: %q arrives after RegisterEOCapabilities sealed the registry. "+
			"Every capability is registered there, at the top of cmd/server/main.go, BEFORE database.Connect, "+
			"so that none can be constructed holding a POOLED repository: three of a capability's four record "+
			"hooks run inside a transaction that holds a FOR UPDATE row lock, and a pooled read taken there "+
			"waits for a free connection forever on a capped pool. Register it in RegisterEOCapabilities and "+
			"read other records through the EOCapabilityReader the engine hands each hook.", c.Key()))
	}
	if _, dup := r.caps[c.Key()]; dup {
		panic(fmt.Sprintf("eo: RegisterCapability: %q is already registered", c.Key()))
	}
	r.caps[c.Key()] = c
}

// seal closes the registry to further registration. It is what makes "a
// capability cannot hold a pooled repository" a property of the code rather
// than a convention: without it, RegisterCapability is exported and the
// registry is mutable at any time, so the natural place to wire a
// repository-backed capability would be the EO block of main, several hundred
// lines below, where eoRecordRepo and eoTypeRepo are in scope and the deadlock
// is one constructor argument away. Sealed, that fails the server's boot —
// loudly, at the only moment somebody is looking.
func (r *eoCapabilityRegistry) seal() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sealed = true
}

// eoCapabilityEntry is one element of EORecordType.Capabilities.
type eoCapabilityEntry struct {
	Key    string          `json:"key"`
	Config json.RawMessage `json:"config"`
}

func (r *eoCapabilityRegistry) resolve(t *models.EORecordType) ([]EOEnabledCapability, error) {
	if t == nil || len(t.Capabilities) == 0 {
		return nil, nil
	}
	var entries []eoCapabilityEntry
	dec := json.NewDecoder(bytes.NewReader(t.Capabilities))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("record type %q: capabilities must be a list of {key, config}: %w", t.Key, err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]bool{}
	out := make([]EOEnabledCapability, 0, len(entries))
	for _, e := range entries {
		if seen[e.Key] {
			return nil, fmt.Errorf("record type %q: capability %q is enabled twice", t.Key, e.Key)
		}
		seen[e.Key] = true
		c, ok := r.caps[e.Key]
		if !ok {
			return nil, fmt.Errorf("record type %q: %w %q", t.Key, ErrEOUnknownCapability, e.Key)
		}
		out = append(out, EOEnabledCapability{Capability: c, Config: datatypes.JSON(e.Config)})
	}
	return out, nil
}

var eoDefaultCapabilities = newEOCapabilityRegistry()

// RegisterCapability adds c to the process-wide registry. It panics on an
// empty or duplicate key: both are wiring mistakes that should stop startup.
func RegisterCapability(c Capability) { eoDefaultCapabilities.register(c) }

// CapabilitiesFor resolves the capabilities a record type enables, in the
// order it lists them. It returns an error — never a shorter list — when the
// list is malformed or names an unregistered key. (The plan sketched no error
// return; it is needed because an unknown key must not be skipped.)
func CapabilitiesFor(t *models.EORecordType) ([]EOEnabledCapability, error) {
	return eoDefaultCapabilities.resolve(t)
}

// RegisterEOCapabilities is the explicit wiring call: main calls it once at
// startup, before the database connect. There is deliberately no init()
// registration.
//
// It SEALS the registry on the way out, so this function is the only place a
// capability can be added. See eoCapabilityRegistry.seal for why that matters
// and eoCapabilityRegistry.register for what a late caller is told.
func RegisterEOCapabilities() {
	RegisterCapability(NewEOApprovalCapability())
	RegisterCapability(NewEODecisionsBaseCapability())
	RegisterCapability(NewEOReferralTreeCapability())
	eoDefaultCapabilities.seal()
}

// eoCapabilityResolver is how a service finds a type's capabilities. Nil means
// the process-wide registry; tests inject their own.
type eoCapabilityResolver func(*models.EORecordType) ([]EOEnabledCapability, error)

func (r eoCapabilityResolver) resolve(t *models.EORecordType) ([]EOEnabledCapability, error) {
	if r == nil {
		return CapabilitiesFor(t)
	}
	return r(t)
}

// eoCapabilitiesValidate runs every capability's Validate over rec and merges
// the field errors. A returned error means a capability is misconfigured.
func eoCapabilitiesValidate(ctx context.Context, read EOCapabilityReader, caps []EOEnabledCapability, rec *models.EORecord) (map[string]string, error) {
	out := map[string]string{}
	for _, c := range caps {
		errs, err := c.Validate(ctx, read, rec, c.Config)
		if err != nil {
			return nil, fmt.Errorf("capability %q: %w", c.Key(), err)
		}
		out = eoMergeFieldErrors(out, errs)
	}
	return out, nil
}

// eoCapabilitiesDerive writes every capability's derived values onto rec. They
// go through eoApplyValues, not the request path, so they need no field
// definition — but they still cannot overwrite an engine-owned column.
func eoCapabilitiesDerive(ctx context.Context, read EOCapabilityReader, caps []EOEnabledCapability, rec *models.EORecord) error {
	for _, c := range caps {
		derived, err := c.Derive(ctx, read, rec, c.Config)
		if err != nil {
			return fmt.Errorf("capability %q: %w", c.Key(), err)
		}
		if len(derived) == 0 {
			continue
		}
		if errs := eoApplyValues(rec, derived); len(errs) > 0 {
			return fmt.Errorf("capability %q derived values could not be stored: %v", c.Key(), errs)
		}
	}
	return nil
}

// eoCapabilitiesCheckConfig refuses a configuration a capability cannot work
// with: a field definition or a transition field change that names one of its
// reserved keys, or a config that references a transition the workflow does
// not have. wf and tr may be nil (the save path has no transition to check).
//
// tr is the EXECUTING transition, as FindTransitionByIDWithRelations loaded it:
// that is the one whose FieldChanges applyFieldChanges admits, and the only
// place they are reliably populated — the workflow's preloaded Transitions
// carry no FieldChanges, so scanning wf.Transitions would never match.
func eoCapabilitiesCheckConfig(caps []EOEnabledCapability, fields []models.EOFieldDef, wf *models.Workflow, tr *models.WorkflowTransition) error {
	for _, c := range caps {
		reserved, err := c.ReservedKeys(c.Config)
		if err != nil {
			return fmt.Errorf("capability %q: %w", c.Key(), err)
		}
		for _, key := range reserved {
			for _, f := range fields {
				if f.Key == key {
					return fmt.Errorf("capability %q: %w: field %q is written by the capability and must not be defined", c.Key(), ErrEOCapabilityConfig, key)
				}
			}
			if tr == nil {
				continue
			}
			for _, fc := range tr.FieldChanges {
				if fc.FieldName == key {
					return fmt.Errorf("capability %q: %w: transition %q lets a request write %q, which the capability owns", c.Key(), ErrEOCapabilityConfig, tr.Code, key)
				}
			}
		}
		if wf != nil {
			if err := c.ValidateWorkflow(c.Config, wf); err != nil {
				return fmt.Errorf("capability %q: %w", c.Key(), err)
			}
		}
	}
	return nil
}

// ── the read seam ───────────────────────────────────────────────────────────

// EOCapabilityReader is how a capability hook reaches a record other than the
// one it was called about: decisions_base reads records of a type other than
// the draft's, referral_tree reads a letter's referrals, their sub-referrals
// and the typed links between them. It is read-only on purpose — a capability
// writes through the values it derives and through OnTransition's mutation of
// rec, never behind the pipeline's back.
//
// WHY A HANDED-IN READER RATHER THAN A REPOSITORY ON THE CAPABILITY.
// A capability is a process-wide singleton, registered at startup before any
// database exists (cmd/server/main.go). If it carried a repository, that
// repository would be the POOLED one, and three of its four hooks run INSIDE
// EORecordRepository.Transaction while that transaction holds its connection
// and a FOR UPDATE row lock. A second pooled read taken there waits for a free
// connection forever on a capped pool — no timeout, no error, a hung request.
// Plan 1 hit that in three separate tasks before fixing the class rather than
// the instances.
//
// So the engine binds the reader per call, to the handles that are safe where
// the hook actually runs:
//
//   - inside a transaction (Update, Execute, and the timer arming at the end
//     of Create): the transaction's own record and type handles;
//   - on the create path before the transaction opens, where the capability
//     list and the effective fields are already read the same way: the pooled
//     ones, because no transaction is open to starve.
//
// A capability therefore cannot take a pooled read inside a transaction
// without first giving itself a repository, which registration order makes
// impossible. Spec §7.7 forbids SQL outside the record repository, so a
// capability that needs a query this interface does not expose gets a
// repository method and one more method here — not a *gorm.DB.
type EOCapabilityReader interface {
	// Record returns any record by id, across every partition, or nil when
	// there is none.
	Record(ctx context.Context, id uuid.UUID) (*models.EORecord, error)
	// RecordType resolves a record type by key, or nil when there is none.
	RecordType(ctx context.Context, key string) (*models.EORecordType, error)
	// Children returns the DIRECT children of parentID. An empty
	// recordTypeKey means every type; a key that names no type is an error,
	// never an empty result, because a typo must not look like "no children".
	Children(ctx context.Context, parentID uuid.UUID, recordTypeKey string) ([]models.EORecord, error)
	// Links returns the typed links declared FROM recordID — the direction a
	// child uses to name its parent, which is how a referral tree is walked.
	Links(ctx context.Context, recordID uuid.UUID) ([]models.EORecordLink, error)
	// RecordsOfType returns every record of one type, oldest first — the
	// standing base decisions_base checks a draft against. It applies no
	// row-level visibility, like Record above: a derived value must not
	// depend on who saved the record, so a capability that must not pull up
	// a restricted record filters on the RECORD'S own declared audience and
	// says so (see eo_capability_decisions_base.go). An empty key, or one
	// that names no type, is an error, never an empty base, because a typo
	// must not look like "nothing standing".
	RecordsOfType(ctx context.Context, recordTypeKey string) ([]models.EORecord, error)
}

// eoCapabilityReader binds the interface above to one pair of repository
// handles. eoNewCapabilityReader is called with the TRANSACTION's handles
// wherever a transaction is open, and with the pooled ones only where none is.
type eoCapabilityReader struct {
	records repository.EORecordRepository
	types   repository.EORecordTypeRepository
}

func eoNewCapabilityReader(records repository.EORecordRepository, types repository.EORecordTypeRepository) EOCapabilityReader {
	return &eoCapabilityReader{records: records, types: types}
}

func (d *eoCapabilityReader) Record(ctx context.Context, id uuid.UUID) (*models.EORecord, error) {
	return d.records.GetAnyByID(ctx, id)
}

func (d *eoCapabilityReader) RecordType(ctx context.Context, key string) (*models.EORecordType, error) {
	return d.types.GetByKey(ctx, key)
}

func (d *eoCapabilityReader) Children(ctx context.Context, parentID uuid.UUID, recordTypeKey string) ([]models.EORecord, error) {
	if strings.TrimSpace(recordTypeKey) == "" {
		return d.records.Children(ctx, parentID, nil)
	}
	t, err := d.types.GetByKey(ctx, recordTypeKey)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("no record type with key %q", recordTypeKey)
	}
	return d.records.Children(ctx, parentID, &t.ID)
}

func (d *eoCapabilityReader) RecordsOfType(ctx context.Context, recordTypeKey string) ([]models.EORecord, error) {
	if strings.TrimSpace(recordTypeKey) == "" {
		return nil, fmt.Errorf("records of type: no record type key")
	}
	t, err := d.types.GetByKey(ctx, recordTypeKey)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("no record type with key %q", recordTypeKey)
	}
	return d.records.RecordsOfType(ctx, t.ID)
}

func (d *eoCapabilityReader) Links(ctx context.Context, recordID uuid.UUID) ([]models.EORecordLink, error) {
	return d.records.LinksFrom(ctx, recordID)
}

// ── embeddable no-ops ───────────────────────────────────────────────────────
//
// ReservedKeys and ValidateWorkflow joined the interface late in Plan 1 and
// most capabilities have nothing to say for either: §7.9 still owes
// quorum_voting, calendar, agenda and numbering. These two save them writing
// an empty method each.
//
// They are deliberately TWO structs rather than one base. A single
// "eoCapabilityBase" would let a capability that genuinely owns a sealed values
// key inherit "reserves nothing" without anyone writing the words — and
// ReservedKeys is the whole of the approval stamp's forgery guard
// (eoCapabilitiesCheckConfig). Split, embedding one is a claim in its own
// right, visible at the embed site and reviewable there.

// eoReservesNoKeys supplies ReservedKeys for a capability that writes no
// values key of its own. Do NOT embed it in a capability that writes a key the
// request path must never be able to set: that is what ReservedKeys exists to
// declare, and inheriting "none" here silently removes the guard.
type eoReservesNoKeys struct{}

func (eoReservesNoKeys) ReservedKeys(datatypes.JSON) ([]string, error) { return nil, nil }

// eoConstrainsNoWorkflow supplies ValidateWorkflow for a capability whose
// config names nothing the workflow has to contain. Do not embed it in a
// capability whose config names transition codes or state codes: the check is
// what turns a typo into a startup-time refusal instead of a hook that never
// fires.
type eoConstrainsNoWorkflow struct{}

func (eoConstrainsNoWorkflow) ValidateWorkflow(datatypes.JSON, *models.Workflow) error { return nil }
