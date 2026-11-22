package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// eoCapFake embeds the interface and overrides only Key: the registry never
// calls anything else.
type eoCapFake struct {
	Capability
	key string
}

func (f eoCapFake) Key() string { return f.key }

func eoCapType(t *testing.T, caps any) *models.EORecordType {
	t.Helper()
	b, err := json.Marshal(caps)
	if err != nil {
		t.Fatal(err)
	}
	return &models.EORecordType{Key: "eo_cap_type", Capabilities: b}
}

func TestRegisterAndResolveCapabilities(t *testing.T) {
	reg := newEOCapabilityRegistry()
	reg.register(eoCapFake{key: "alpha"})
	reg.register(eoCapFake{key: "beta"})

	t.Run("resolves in the type's order with each config", func(t *testing.T) {
		got, err := reg.resolve(eoCapType(t, []map[string]any{
			{"key": "beta", "config": map[string]any{"n": 2}},
			{"key": "alpha"},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Key() != "beta" || got[1].Key() != "alpha" {
			t.Fatalf("order not preserved: %+v", got)
		}
		if string(got[0].Config) != `{"n":2}` {
			t.Fatalf("config not bound: %s", got[0].Config)
		}
	})
	t.Run("a type with no capabilities resolves to none", func(t *testing.T) {
		got, err := reg.resolve(&models.EORecordType{})
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("an unknown key is an error, not a skip", func(t *testing.T) {
		got, err := reg.resolve(eoCapType(t, []map[string]any{{"key": "alpha"}, {"key": "nope"}}))
		if !errors.Is(err, ErrEOUnknownCapability) {
			t.Fatalf("want ErrEOUnknownCapability, got %v (%v)", err, got)
		}
	})
	t.Run("a malformed list is an error", func(t *testing.T) {
		for name, raw := range map[string]string{
			"not a list":    `{"key":"alpha"}`,
			"bare string":   `["alpha"]`,
			"unknown field": `[{"key":"alpha","cfg":{}}]`,
		} {
			_, err := reg.resolve(&models.EORecordType{Capabilities: datatypes.JSON(raw)})
			if err == nil {
				t.Errorf("%s: want error", name)
			}
		}
	})
	t.Run("the same key twice is an error", func(t *testing.T) {
		if _, err := reg.resolve(eoCapType(t, []map[string]any{{"key": "alpha"}, {"key": "alpha"}})); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("registering a key twice panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("want panic")
			}
		}()
		reg.register(eoCapFake{key: "alpha"})
	})
	t.Run("the wiring call registers approval and is not an init", func(t *testing.T) {
		saved := eoDefaultCapabilities
		eoDefaultCapabilities = newEOCapabilityRegistry()
		defer func() { eoDefaultCapabilities = saved }()
		typ := eoCapType(t, []map[string]any{{"key": EOApprovalKey, "config": map[string]any{"transitions": []string{"x"}}}})
		if _, err := CapabilitiesFor(typ); !errors.Is(err, ErrEOUnknownCapability) {
			t.Fatalf("registered before wiring: %v", err)
		}
		RegisterEOCapabilities()
		got, err := CapabilitiesFor(typ)
		if err != nil || len(got) != 1 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
}

var eoApprovalTestCfg = datatypes.JSON(`{"transitions":["approve"]}`)

func eoApprovalFixture(t *testing.T, values map[string]any) (Capability, *models.EORecord, *models.User, context.Context) {
	t.Helper()
	b, _ := json.Marshal(values)
	c := &eoApprovalCapability{now: func() time.Time { return time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC) }}
	actor := &models.User{ID: uuid.New()}
	return c, &models.EORecord{Reference: "EO-1", Values: b}, actor, WithEOActor(context.Background(), actor)
}

func eoApprovalStampOf(t *testing.T, rec *models.EORecord) map[string]any {
	t.Helper()
	s, _ := eoDecodeValues(rec.Values)["approval"].(map[string]any)
	return s
}

func TestApprovalStampRecordsBothIdentities(t *testing.T) {
	c, rec, actor, ctx := eoApprovalFixture(t, map[string]any{
		"approval_authority":       "H.E. the Secretary",
		"approval_delegation_note": "by delegation of H.E., minute 4",
		"other":                    "kept",
	})
	if err := c.OnTransition(ctx, nil, rec, &models.WorkflowTransition{Code: "approve"}, eoApprovalTestCfg); err != nil {
		t.Fatal(err)
	}
	s := eoApprovalStampOf(t, rec)
	if s["recorded_by"] != actor.ID.String() {
		t.Errorf("recorded_by = %v, want the actor %s", s["recorded_by"], actor.ID)
	}
	if s["authority"] != "H.E. the Secretary" || s["authority"] == s["recorded_by"] {
		t.Errorf("authority = %v", s["authority"])
	}
	if s["delegation_note"] != "by delegation of H.E., minute 4" {
		t.Errorf("delegation_note = %v", s["delegation_note"])
	}
	if s["recorded_at"] != "2026-11-22T09:00:00Z" {
		t.Errorf("recorded_at = %v", s["recorded_at"])
	}
	if eoDecodeValues(rec.Values)["other"] != "kept" {
		t.Error("the stamp clobbered other values")
	}

	t.Run("a transition not listed does not stamp", func(t *testing.T) {
		c, rec, _, ctx := eoApprovalFixture(t, map[string]any{"approval_authority": "H.E."})
		if err := c.OnTransition(ctx, nil, rec, &models.WorkflowTransition{Code: "other"}, eoApprovalTestCfg); err != nil {
			t.Fatal(err)
		}
		if eoApprovalStampOf(t, rec) != nil {
			t.Error("stamped on an unlisted transition")
		}
	})
	t.Run("no acting user is an error", func(t *testing.T) {
		c, rec, _, _ := eoApprovalFixture(t, map[string]any{"approval_authority": "H.E."})
		if err := c.OnTransition(context.Background(), nil, rec, &models.WorkflowTransition{Code: "approve"}, eoApprovalTestCfg); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestApprovalStampRejectsMissingAuthority(t *testing.T) {
	for name, values := range map[string]map[string]any{
		"absent":     {},
		"empty":      {"approval_authority": ""},
		"whitespace": {"approval_authority": "   "},
		"not text":   {"approval_authority": 7},
	} {
		t.Run(name, func(t *testing.T) {
			c, rec, _, ctx := eoApprovalFixture(t, values)
			before := string(rec.Values)
			err := c.OnTransition(ctx, nil, rec, &models.WorkflowTransition{Code: "approve"}, eoApprovalTestCfg)
			var ve *EOValidationError
			if !errors.As(err, &ve) || ve.Fields["approval_authority"] == "" {
				t.Fatalf("want a validation error on approval_authority, got %v", err)
			}
			if string(rec.Values) != before {
				t.Error("a stamp was written without an authority (defaulted to the actor?)")
			}
		})
	}
}

func TestApprovalStampIsImmutableOnceSet(t *testing.T) {
	c, rec, _, ctx := eoApprovalFixture(t, map[string]any{"approval_authority": "H.E."})
	tr := &models.WorkflowTransition{Code: "approve"}
	if err := c.OnTransition(ctx, nil, rec, tr, eoApprovalTestCfg); err != nil {
		t.Fatal(err)
	}
	sealed := string(rec.Values)

	if err := c.OnTransition(ctx, nil, rec, tr, eoApprovalTestCfg); err == nil {
		t.Error("a second stamp replaced the first")
	}
	if string(rec.Values) != sealed {
		t.Error("the sealed stamp changed")
	}

	t.Run("unchanged inputs validate", func(t *testing.T) {
		errs, err := c.Validate(ctx, nil, rec, eoApprovalTestCfg)
		if err != nil || len(errs) != 0 {
			t.Fatalf("%v %v", errs, err)
		}
	})
	t.Run("an edited authority is rejected", func(t *testing.T) {
		bag := eoDecodeValues(rec.Values)
		bag["approval_authority"] = "Head of Office"
		rec.Values, _ = json.Marshal(bag)
		errs, err := c.Validate(ctx, nil, rec, eoApprovalTestCfg)
		if err != nil || errs["approval_authority"] == "" {
			t.Fatalf("%v %v", errs, err)
		}
	})
	t.Run("an edited delegation note is rejected", func(t *testing.T) {
		bag := eoDecodeValues(rec.Values)
		bag["approval_authority"] = "H.E."
		bag["approval_delegation_note"] = "added later"
		rec.Values, _ = json.Marshal(bag)
		errs, _ := c.Validate(ctx, nil, rec, eoApprovalTestCfg)
		if errs["approval_delegation_note"] == "" {
			t.Fatalf("%v", errs)
		}
	})
}

func TestApprovalConfigMalformedIsAnErrorNotAPanic(t *testing.T) {
	for name, cfg := range map[string]string{
		"empty":          ``,
		"not json":       `{`,
		"wrong type":     `{"transitions":"approve"}`,
		"unknown key":    `{"transitions":["a"],"stampkey":"x"}`,
		"no transitions": `{"stamp_key":"approval"}`,
		"json null":      `null`,
		"array":          `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			c, rec, _, ctx := eoApprovalFixture(t, map[string]any{"approval_authority": "H.E."})
			tr := &models.WorkflowTransition{Code: "approve"}
			if err := c.OnTransition(ctx, nil, rec, tr, datatypes.JSON(cfg)); err == nil {
				t.Error("OnTransition: want error")
			}
			if _, err := c.Validate(ctx, nil, rec, datatypes.JSON(cfg)); err == nil {
				t.Error("Validate: want error")
			}
		})
	}
}

// ── the capability read seam (Task B1) ──────────────────────────────────────

// eoReadCapKey is the registry key of the probe capability below.
const eoReadCapKey = "eo_read_probe"

// eoReadCap is a capability whose every record-facing hook reaches for ANOTHER
// record through the reader the engine hands it. It is the shape Tasks B2 and
// B3 will have: decisions_base reads standing records of a type other than the
// draft's, referral_tree reads a letter's referrals and their typed links.
//
// It deliberately does NOT embed Capability. The two no-op bases complete its
// method set, which is what those bases are for — and it means a method added
// to Capability breaks this build instead of panicking at run time.
type eoReadCap struct {
	eoReservesNoKeys
	eoConstrainsNoWorkflow

	// childTypeKey is the record type whose children the probe reads.
	childTypeKey string
	// otherID is a record of another type, read by id.
	otherID uuid.UUID

	// readsBy is hook name -> children seen. A hook missing from it never
	// read, so an assertion about it would prove nothing.
	readsBy map[string]int
	// failures names hooks the engine handed no reader at all — a seam that
	// compiles and cannot be reached from the pipeline.
	failures []string
}

func (c *eoReadCap) Key() string { return eoReadCapKey }

// reach exercises all four reader methods, so every one of them is proved
// against the handle the engine chose.
func (c *eoReadCap) reach(ctx context.Context, hook string, read EOCapabilityReader, rec *models.EORecord) (int, error) {
	if c.readsBy == nil {
		c.readsBy = map[string]int{}
	}
	if read == nil {
		c.failures = append(c.failures, hook)
		return 0, fmt.Errorf("%s: the engine handed this capability no reader", hook)
	}
	kids, err := read.Children(ctx, rec.ID, c.childTypeKey)
	if err != nil {
		return 0, err
	}
	if c.otherID != uuid.Nil {
		if _, err := read.Record(ctx, c.otherID); err != nil {
			return 0, err
		}
	}
	if _, err := read.Links(ctx, rec.ID); err != nil {
		return 0, err
	}
	if _, err := read.RecordType(ctx, c.childTypeKey); err != nil {
		return 0, err
	}
	c.readsBy[hook] = len(kids)
	return len(kids), nil
}

func (c *eoReadCap) Validate(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, _ datatypes.JSON) (map[string]string, error) {
	_, err := c.reach(ctx, "Validate", read, rec)
	return nil, err
}

func (c *eoReadCap) Derive(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, _ datatypes.JSON) (map[string]any, error) {
	n, err := c.reach(ctx, "Derive", read, rec)
	if err != nil {
		return nil, err
	}
	return map[string]any{"eo_read_probe_children": n}, nil
}

func (c *eoReadCap) OnTransition(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, _ *models.WorkflowTransition, _ datatypes.JSON) error {
	_, err := c.reach(ctx, "OnTransition", read, rec)
	return err
}

func (c *eoReadCap) Timers(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, _ datatypes.JSON) ([]models.EOTimer, error) {
	_, err := c.reach(ctx, "Timers", read, rec)
	return nil, err
}

// eoReadCapRan asserts the probe reached every hook the path runs, so an
// emptiness assertion elsewhere cannot pass because nothing happened.
func eoReadCapRan(t *testing.T, c *eoReadCap, hooks ...string) {
	t.Helper()
	if len(c.failures) != 0 {
		t.Fatalf("hooks run with no reader at all: %v", c.failures)
	}
	for _, h := range hooks {
		if _, ok := c.readsBy[h]; !ok {
			t.Errorf("%s never read, so this test proves nothing for it", h)
		}
	}
}

// ── the save pipeline's repositories, told apart ────────────────────────────
//
// The transition fixture already distinguishes the transaction's handles from
// the pooled ones (eoInTxRecords/eoInTxTypes). These do the same for the save
// pipeline, whose fake hands its closure the fake itself unless told otherwise.

type eoReadPooledRecords struct {
	*eoSvcRecordRepo
	open *bool
	inTx *[]string
}

func (r eoReadPooledRecords) note(name string) {
	if *r.open {
		*r.inTx = append(*r.inTx, name)
	}
}

func (r eoReadPooledRecords) Children(ctx context.Context, parentID uuid.UUID, recordTypeID *uuid.UUID) ([]models.EORecord, error) {
	r.note("Children")
	return r.eoSvcRecordRepo.Children(ctx, parentID, recordTypeID)
}

func (r eoReadPooledRecords) GetAnyByID(ctx context.Context, id uuid.UUID) (*models.EORecord, error) {
	r.note("GetAnyByID")
	return r.eoSvcRecordRepo.GetAnyByID(ctx, id)
}

func (r eoReadPooledRecords) LinksFrom(ctx context.Context, recordID uuid.UUID) ([]models.EORecordLink, error) {
	r.note("LinksFrom")
	return r.eoSvcRecordRepo.LinksFrom(ctx, recordID)
}

func (r eoReadPooledRecords) Transaction(ctx context.Context, fn func(repository.EORecordRepository, repository.EORecordTypeRepository, repository.EOTimerRepository) error) error {
	*r.open = true
	defer func() { *r.open = false }()
	return r.eoSvcRecordRepo.Transaction(ctx, fn)
}

type eoReadPooledTypes struct {
	*eoSvcTypeRepo
	open *bool
	inTx *[]string
}

func (r eoReadPooledTypes) GetByKey(ctx context.Context, key string) (*models.EORecordType, error) {
	if *r.open {
		*r.inTx = append(*r.inTx, "GetByKey")
	}
	return r.eoSvcTypeRepo.GetByKey(ctx, key)
}

// eoReadBoundRecords / eoReadBoundTypes are what Transaction hands its
// closure: the same store, a DISTINCT object, so a read that reaches the
// pooled wrapper above can be told apart from one taken through the
// transaction.
type eoReadBoundRecords struct{ *eoSvcRecordRepo }

type eoReadBoundTypes struct{ *eoSvcTypeRepo }

// eoReadSvcFixture rebuilds a save-pipeline fixture whose pooled and
// transaction-bound handles are distinguishable, and returns the list of
// pooled reads taken while a transaction was open. That list must stay empty:
// every entry in it is a second connection taken while the transaction holds
// its own plus a FOR UPDATE row lock.
func eoReadSvcFixture(t *testing.T, c *eoReadCap, defs ...models.EOFieldDef) (*eoSvcFixture, *[]string) {
	t.Helper()
	f := eoNewSvcFixture(t, defs...)
	f.records.children = map[uuid.UUID][]models.EORecord{}
	f.records.txRecords = eoReadBoundRecords{f.records}
	f.records.txTypes = eoReadBoundTypes{f.types}

	open := false
	inTx := &[]string{}
	f.svc = NewEORecordService(
		eoReadPooledRecords{eoSvcRecordRepo: f.records, open: &open, inTx: inTx},
		eoReadPooledTypes{eoSvcTypeRepo: f.types, open: &open, inTx: inTx},
		f.sequences, f.workflows)
	f.svc.(*eoRecordService).caps = eoWireResolver(c)
	return f, inTx
}

// TestCapabilityReadsGoThroughTheTransactionBoundHandle is Plan 1's pooled-read
// probe extended to the capability hooks. Plan 1 proved it for the effective
// fields, the capability list and the derived roll-ups; this proves it for the
// read path Task B1 opens, on all three pipelines, through the real services —
// never through a hand-built hook call.
//
// The failure it exists to catch: a hook handed a POOLED repository while the
// transaction holds its own connection plus a FOR UPDATE row lock. database/sql
// then waits for a free connection forever on a capped pool — no timeout, no
// error, a hung request.
func TestCapabilityReadsGoThroughTheTransactionBoundHandle(t *testing.T) {
	t.Run("transition: every hook reads through the transaction's handles", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.records.txTimers = &eoBridgeTimers{}
		stage := &models.EORecordType{ID: uuid.New(), Key: "eo_stage", Name: "Stage", IsActive: true}
		f.types.types[stage.ID] = stage
		f.types.linkTypes["note_on"] = &models.EOLinkType{Key: "note_on"}
		f.addChild("note_on", false)
		f.records.children[f.rec.ID][0].RecordTypeID = stage.ID
		other := &models.EORecord{ID: uuid.New(), RecordTypeID: stage.ID, Reference: "EO-STG-2026-001"}
		f.records.stored[other.ID] = other

		c := &eoReadCap{childTypeKey: "eo_stage", otherID: other.ID}
		open := false
		var inTx []string
		svc := NewEOTransitionService(
			eoInTxRecords{f.records, &open},
			eoInTxTypes{eoTxTypeRepo: f.types, open: &open, inTx: &inTx},
			f.workflows, f.actions)
		svc.(*eoTransitionService).caps = eoWireResolver(c)

		got, err := svc.Execute(context.Background(), f.rec, f.transition("raise").ID, f.actor, models.EOTransitionInput{})
		if err != nil {
			t.Fatalf("raise: %v", err)
		}
		eoReadCapRan(t, c, "Validate", "OnTransition", "Derive", "Timers")
		if eoSvcValues(t, got.Values)["eo_read_probe_children"] != 1.0 {
			t.Fatalf("the capability's read never reached the record: %s", got.Values)
		}
		if len(inTx) != 0 {
			t.Errorf("pooled type reads inside the transaction: %v", inTx)
		}
		if f.records.childrenTotal == 0 || f.records.getAnyTotal == 0 || f.records.linksTotal == 0 {
			t.Fatalf("children=%d ancestors=%d links=%d; the probe read nothing",
				f.records.childrenTotal, f.records.getAnyTotal, f.records.linksTotal)
		}
		if f.records.childrenInTx != f.records.childrenTotal {
			t.Errorf("child reads: %d of %d went through the transaction — the rest took a second pooled connection while this one holds a row lock",
				f.records.childrenInTx, f.records.childrenTotal)
		}
		if f.records.getAnyInTx != f.records.getAnyTotal {
			t.Errorf("record-by-id reads: %d of %d went through the transaction",
				f.records.getAnyInTx, f.records.getAnyTotal)
		}
		if f.records.linksInTx != f.records.linksTotal {
			t.Errorf("link reads: %d of %d went through the transaction",
				f.records.linksInTx, f.records.linksTotal)
		}
	})

	t.Run("update: the hooks run inside the transaction and read through its handles", func(t *testing.T) {
		c := &eoReadCap{childTypeKey: "eo_stage"}
		f, inTx := eoReadSvcFixture(t, c, eoSvcField("note", models.EORequiredNever))
		stage := f.types.add(&models.EORecordType{Key: "eo_stage", Name: "Stage", IsActive: true})
		rec := eoSvcExisting(f, nil)
		f.records.children[rec.ID] = []models.EORecord{{ID: uuid.New(), RecordTypeID: stage.ID}}
		c.otherID = rec.ID

		got, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
			RecordTypeID: rec.RecordTypeID, Values: map[string]any{"note": "edited"},
		}, f.actor)
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		eoReadCapRan(t, c, "Validate", "Derive")
		if eoSvcValues(t, got.Values)["eo_read_probe_children"] != 1.0 {
			t.Fatalf("the capability's read never reached the record: %s", got.Values)
		}
		if len(*inTx) != 0 {
			t.Errorf("pooled reads inside the transaction: %v", *inTx)
		}
	})

	t.Run("create: the record hooks read before the transaction and the timer hook through it", func(t *testing.T) {
		c := &eoReadCap{childTypeKey: "eo_stage"}
		f, inTx := eoReadSvcFixture(t, c)
		f.types.add(&models.EORecordType{Key: "eo_stage", Name: "Stage", IsActive: true})

		got, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
			RecordTypeID: f.recordType.ID, Values: map[string]any{"subject_ar": "توجيه"},
		}, f.actor)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		// Validate and Derive run before the transaction opens, so a pooled
		// read there is the SAFE one; Timers runs inside it and must not be.
		eoReadCapRan(t, c, "Validate", "Derive", "Timers")
		if _, ok := eoSvcValues(t, got.Values)["eo_read_probe_children"]; !ok {
			t.Fatalf("the capability's read never reached the record: %s", got.Values)
		}
		if len(*inTx) != 0 {
			t.Errorf("pooled reads inside the transaction: %v", *inTx)
		}
	})
}

// TestRegistrationOrderAllowsARepositoryBackedCapability pins the premise the
// whole design rests on. A capability that needs to read other records is
// registered where registration happens today — at cmd/server/main.go, BEFORE
// database.Connect — and still resolves and reads, because the read path is
// handed to it per call by the engine rather than built into it at
// construction. Nothing is "repository-backed"; that is the point, and the
// second subtest is what keeps it that way.
func TestRegistrationOrderAllowsARepositoryBackedCapability(t *testing.T) {
	t.Run("a capability that needs other records registers with no database and resolves", func(t *testing.T) {
		saved := eoDefaultCapabilities
		eoDefaultCapabilities = newEOCapabilityRegistry()
		defer func() { eoDefaultCapabilities = saved }()
		// The real wiring call, running with no *gorm.DB in scope, exactly as
		// main does it.
		RegisterEOCapabilities()
		c := &eoReadCap{childTypeKey: "eo_stage"}
		RegisterCapability(c)

		f := eoNewTxFixture(t)
		f.records.txTimers = &eoBridgeTimers{}
		stage := &models.EORecordType{ID: uuid.New(), Key: "eo_stage", Name: "Stage", IsActive: true}
		f.types.types[stage.ID] = stage
		f.types.linkTypes["note_on"] = &models.EOLinkType{Key: "note_on"}
		f.addChild("note_on", false)
		f.records.children[f.rec.ID][0].RecordTypeID = stage.ID
		// No caps override: the pipeline resolves through the real registry.
		f.recordType.Capabilities = datatypes.JSON(`[{"key":"` + eoReadCapKey + `"}]`)

		got, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if err != nil {
			t.Fatalf("raise: %v", err)
		}
		eoReadCapRan(t, c, "Validate", "OnTransition", "Derive", "Timers")
		if eoSvcValues(t, got.Values)["eo_read_probe_children"] != 1.0 {
			t.Fatalf("a registered, data-needing capability resolved but read nothing: %s", got.Values)
		}
	})

	// A source-level test, in the shape of database.TestMigrateReturnsTheEOPlatformMigrationError.
	// The ordering is the guard: at main.go:42 there is no *gorm.DB and no
	// repository in scope, so a capability CANNOT be constructed with a pooled
	// one. Move registration after the connect and that guard is gone — which
	// is precisely the single-line change that makes this fail.
	t.Run("registration still runs before the database connect in main", func(t *testing.T) {
		const src = "../../cmd/server/main.go"
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, src, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", src, err)
		}
		var body []ast.Stmt
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "main" && fn.Recv == nil {
				body = fn.Body.List
			}
		}
		if body == nil {
			t.Fatalf("%s has no func main", src)
		}
		at := func(pkg, name string) int {
			for i, stmt := range body {
				found := false
				ast.Inspect(stmt, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != name {
						return true
					}
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
						found = true
					}
					return true
				})
				if found {
					return i
				}
			}
			return -1
		}
		register, connect := at("services", "RegisterEOCapabilities"), at("database", "Connect")
		if register < 0 || connect < 0 {
			t.Fatalf("main.go: RegisterEOCapabilities at %d, database.Connect at %d; both must be top-level statements of main", register, connect)
		}
		if register > connect {
			t.Errorf("services.RegisterEOCapabilities() runs AFTER database.Connect(). A capability can now be constructed with a pooled repository, which is the deadlock this task exists to prevent; capability reads go through the reader the engine hands each hook instead.")
		}
	})
}

// eoMergeCap reports one message on one field key and does nothing else. The
// two no-op bases supply ReservedKeys and ValidateWorkflow.
type eoMergeCap struct {
	eoReservesNoKeys
	eoConstrainsNoWorkflow
	key, field, msg string
}

func (c *eoMergeCap) Key() string { return c.key }

func (c *eoMergeCap) Validate(context.Context, EOCapabilityReader, *models.EORecord, datatypes.JSON) (map[string]string, error) {
	return map[string]string{c.field: c.msg}, nil
}

func (c *eoMergeCap) Derive(context.Context, EOCapabilityReader, *models.EORecord, datatypes.JSON) (map[string]any, error) {
	return nil, nil
}

func (c *eoMergeCap) OnTransition(context.Context, EOCapabilityReader, *models.EORecord, *models.WorkflowTransition, datatypes.JSON) error {
	return nil
}

func (c *eoMergeCap) Timers(context.Context, EOCapabilityReader, *models.EORecord, datatypes.JSON) ([]models.EOTimer, error) {
	return nil, nil
}

// TestTwoCapabilitiesOnOneFieldKeyMergeFirstWins is Review Focus 5. Field
// errors merge with "first explanation wins", so two capabilities reporting on
// one key makes an order load-bearing. The order that decides must be the one
// the RECORD TYPE lists — not map iteration, and not registration order.
//
// Registration order is held constant across both cases and only the type's
// list is reversed, so registration order cannot be what produces the
// difference; the registry resolves a list, not a map, so iteration order
// cannot either.
func TestTwoCapabilitiesOnOneFieldKeyMergeFirstWins(t *testing.T) {
	for _, tc := range []struct {
		name   string
		listed string
		want   string
	}{
		{"the type lists alpha first", `[{"key":"eo_merge_alpha"},{"key":"eo_merge_beta"}]`, "alpha explains it"},
		{"the type lists beta first", `[{"key":"eo_merge_beta"},{"key":"eo_merge_alpha"}]`, "beta explains it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := eoDefaultCapabilities
			eoDefaultCapabilities = newEOCapabilityRegistry()
			defer func() { eoDefaultCapabilities = saved }()
			// Alpha is always registered first, in both cases.
			RegisterCapability(&eoMergeCap{key: "eo_merge_alpha", field: "subject_ar", msg: "alpha explains it"})
			RegisterCapability(&eoMergeCap{key: "eo_merge_beta", field: "subject_ar", msg: "beta explains it"})

			f := eoNewSvcFixture(t)
			// No caps override: resolution goes through the real registry, in
			// the order the record type lists.
			f.recordType.Capabilities = datatypes.JSON(tc.listed)
			_, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
				RecordTypeID: f.recordType.ID, Values: map[string]any{"subject_ar": "توجيه"},
			}, f.actor)
			if got := eoSvcFieldErrors(t, err)["subject_ar"]; got != tc.want {
				t.Errorf("subject_ar = %q, want %q — the type's listed order must decide which explanation the user sees", got, tc.want)
			}
			if len(f.records.created) != 0 {
				t.Errorf("records created = %d, want none", len(f.records.created))
			}
		})
	}
}
