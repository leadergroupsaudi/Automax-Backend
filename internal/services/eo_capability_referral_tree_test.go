package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// eoRefTreeReader stands in for the reader the engine hands each hook. It
// EMBEDS the interface and overrides only Children — the one method this
// capability calls — which is the convention for repository-shaped fakes: a
// fake must not become more capable than the thing it stands in for. (The note
// at eoReadCap explains why CAPABILITY fakes go the other way; this is not
// one.)
//
// Leaving every other method nil is load-bearing, not laziness: a capability
// that grew a second read would panic here rather than quietly take it, and
// "exactly one read" is a property this task's tests assert.
type eoRefTreeReader struct {
	EOCapabilityReader
	// children is parent id -> record type key -> the children of that type,
	// in the order the repository returns them (oldest first).
	children map[uuid.UUID]map[string][]models.EORecord
	// asked records every (parent, type key) the capability read, so a test
	// can prove the capability did NOT read as well as that it did.
	asked []string
}

func (r *eoRefTreeReader) Children(_ context.Context, parentID uuid.UUID, key string) ([]models.EORecord, error) {
	r.asked = append(r.asked, key)
	byKey, ok := r.children[parentID]
	if !ok {
		byKey = map[string][]models.EORecord{}
	}
	recs, ok := byKey[key]
	if !ok {
		// The reader's own contract: a key that names no type is an error,
		// never an empty result, because a typo must not look like "no
		// branches".
		return nil, fmt.Errorf("no record type with key %q", key)
	}
	return recs, nil
}

// eoRefTreeBranchTypeKey is the branch type in these tests and eoRefTreeLetter
// builds the record the capability is called about. They are the pilot's keys,
// but nothing in the capability knows them: they arrive as configuration.
const eoRefTreeBranchTypeKey = "eo_referral"

// eoRefTreeTargetKey is the capability's default target, spelled out so a test
// that renames it still reads.
const eoRefTreeTargetKey = "referral_tree"

func eoRefTreeLetter(t *testing.T, values map[string]any) *models.EORecord {
	t.Helper()
	rec := eoRefTreeRec(t, "IN-2026-0001", values)
	return &rec
}

// eoRefTreeRec builds a stored record with these values. CreatedAt is never
// set in Go.
func eoRefTreeRec(t *testing.T, reference string, values map[string]any) models.EORecord {
	t.Helper()
	if values == nil {
		// json.Marshal of a nil map is `null`, and eoDecodeValues decodes
		// that to a NIL map — which eoApplyValues then panics writing to.
		// A record the engine created never holds null (an empty Values is
		// an empty byte slice), so the fixture does not create one either.
		values = map[string]any{}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("encode values of %s: %v", reference, err)
	}
	return models.EORecord{ID: uuid.New(), Reference: reference, Values: datatypes.JSON(raw)}
}

// eoRefTreeBranch builds one referral of a letter: answered means the engine
// closed it (a terminal state sets closed_at, eo_transition_service.go), which
// is the same signal BlockingChildren reads. parent is the SIBLING referral it
// descends from, named through the `parent_referral` link FIELD — never a
// record parent, which is always the letter.
func eoRefTreeBranch(t *testing.T, reference string, answered bool, parent *models.EORecord, extra map[string]any) models.EORecord {
	t.Helper()
	values := map[string]any{}
	for k, v := range extra {
		values[k] = v
	}
	if parent != nil {
		values["parent_referral"] = parent.ID.String()
	}
	rec := eoRefTreeRec(t, reference, values)
	if answered {
		closed := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
		rec.ClosedAt = &closed
	}
	return rec
}

// eoRefTreeConfig encodes a capability config blob.
func eoRefTreeConfig(t *testing.T, cfg map[string]any) datatypes.JSON {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	return datatypes.JSON(raw)
}

// eoRefTreeStdConfig is the pilot's configuration: the letter's referrals,
// shaped by the `parent_referral` link field A3 seeded.
func eoRefTreeStdConfig(t *testing.T) datatypes.JSON {
	t.Helper()
	return eoRefTreeConfig(t, map[string]any{"branch_type_key": eoRefTreeBranchTypeKey})
}

// eoRefTreeReaderFor wires a reader whose only letter is rec.
func eoRefTreeReaderFor(rec *models.EORecord, branches []models.EORecord) *eoRefTreeReader {
	return &eoRefTreeReader{children: map[uuid.UUID]map[string][]models.EORecord{
		rec.ID: {eoRefTreeBranchTypeKey: branches},
	}}
}

// ── the stored shape ────────────────────────────────────────────────────────
//
// The views below are the CONTRACT, written out in the test rather than
// imported from the capability: decoding the stored value into them with
// DisallowUnknownFields is what proves the consolidation carries structure and
// status and nothing else. A field added to the stored output — a branch's
// answer, its body, its instruction — fails to decode here.

type eoRefTreeBranchView struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`
	ParentID  string `json:"parent_id"`
	Depth     int    `json:"depth"`
	Answered  bool   `json:"answered"`
	Anomaly   string `json:"anomaly,omitempty"`
}

type eoRefTreeView struct {
	Branches    []eoRefTreeBranchView `json:"branches"`
	Outstanding []string              `json:"outstanding"`
	Complete    bool                  `json:"complete"`
}

// eoRefTreeStored runs Derive, applies what it returned the way the engine
// does, and decodes the STORED value strictly. Testing the stored value rather
// than the returned map is deliberate: the officer and the drafter read what
// is in the record, and a value that cannot survive eoApplyValues is of no use
// to either.
func eoRefTreeStored(t *testing.T, rec *models.EORecord, derived map[string]any, target string) eoRefTreeView {
	t.Helper()
	if errs := eoApplyValues(rec, derived); len(errs) > 0 {
		t.Fatalf("the consolidation could not be stored: %v", errs)
	}
	stored, ok := eoDecodeValues(rec.Values)[target]
	if !ok {
		t.Fatalf("%q was not stored at all; an absent key says 'never consolidated', not 'no branches'", target)
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("re-encode the stored tree: %v", err)
	}
	var out eoRefTreeView
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("the stored tree does not have the declared shape: %v\nstored: %s", err, raw)
	}
	return out
}

// eoRefTreeWant is one expected branch, written in references rather than
// uuids so a failure reads as referral numbers.
type eoRefTreeWant struct {
	reference string
	// parent is the REFERENCE of the branch this one descends from, or ""
	// for a branch that hangs straight off the letter.
	parent   string
	depth    int
	answered bool
	anomaly  string
}

// eoRefTreeFlatten turns the stored tree back into references, failing when it
// names a record that is not a branch of this letter.
func eoRefTreeFlatten(t *testing.T, view eoRefTreeView, branches []models.EORecord) []eoRefTreeWant {
	t.Helper()
	byID := map[string]string{}
	for _, b := range branches {
		byID[b.ID.String()] = b.Reference
	}
	out := make([]eoRefTreeWant, 0, len(view.Branches))
	for _, b := range view.Branches {
		ref, ok := byID[b.ID]
		if !ok {
			t.Fatalf("the tree names %s, which is not a branch of this letter", b.ID)
		}
		if ref != b.Reference {
			t.Errorf("branch %s is stored with reference %q, want %q", b.ID, b.Reference, ref)
		}
		parent := ""
		if b.ParentID != "" {
			parent, ok = byID[b.ParentID]
			if !ok {
				t.Fatalf("branch %s names parent %s, which is not a branch of this letter", ref, b.ParentID)
			}
		}
		out = append(out, eoRefTreeWant{reference: ref, parent: parent, depth: b.Depth, answered: b.Answered, anomaly: b.Anomaly})
	}
	return out
}

// eoRefTreeDerive runs Derive and fails on error.
func eoRefTreeDerive(t *testing.T, c Capability, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) map[string]any {
	t.Helper()
	got, err := c.Derive(context.Background(), read, rec, cfg)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	return got
}

// ── every branch, and the shape the link field gives them ───────────────────

// TestReferralTreeConsolidatesEveryBranch is the task's first rule and the one
// the programme keeps breaking: a consolidation that silently omits a branch
// looks complete, which is worse than looking empty. Every case below asserts
// the COUNT as well as the shape, so a branch dropped for any reason fails
// here whatever else stays green.
//
// The tree's shape comes from the `parent_referral` link FIELD, not from
// record parentage: every referral — sub-referrals included — is a record
// child of the LETTER, because the client's IN-YYYY-NNNN-R# pattern refuses a
// four-segment parent (Task A4, seed_eo_pilot.go).
func TestReferralTreeConsolidatesEveryBranch(t *testing.T) {
	capability := NewEOReferralTreeCapability()
	cfg := eoRefTreeStdConfig(t)

	for _, tc := range []struct {
		name string
		// build returns the letter's referrals in the order the repository
		// returns them, oldest first.
		build func(t *testing.T) []models.EORecord
		want  []eoRefTreeWant
	}{
		{
			name: "a flat direction: every branch hangs off the letter",
			build: func(t *testing.T) []models.EORecord {
				return []models.EORecord{
					eoRefTreeBranch(t, "IN-2026-0001-R1", true, nil, nil),
					eoRefTreeBranch(t, "IN-2026-0001-R2", false, nil, nil),
				}
			},
			want: []eoRefTreeWant{
				{reference: "IN-2026-0001-R1", depth: 0, answered: true},
				{reference: "IN-2026-0001-R2", depth: 0},
			},
		},
		{
			name: "a sub-referral appears in its parent's tree, under the branch it descends from",
			build: func(t *testing.T) []models.EORecord {
				r1 := eoRefTreeBranch(t, "IN-2026-0001-R1", true, nil, nil)
				r2 := eoRefTreeBranch(t, "IN-2026-0001-R2", false, nil, nil)
				r3 := eoRefTreeBranch(t, "IN-2026-0001-R3", true, &r1, nil)
				r4 := eoRefTreeBranch(t, "IN-2026-0001-R4", false, &r3, nil)
				return []models.EORecord{r1, r2, r3, r4}
			},
			want: []eoRefTreeWant{
				{reference: "IN-2026-0001-R1", depth: 0, answered: true},
				{reference: "IN-2026-0001-R3", parent: "IN-2026-0001-R1", depth: 1, answered: true},
				{reference: "IN-2026-0001-R4", parent: "IN-2026-0001-R3", depth: 2},
				{reference: "IN-2026-0001-R2", depth: 0},
			},
		},
		{
			name: "a branch named as a parent before it is itself consolidated keeps the shape",
			build: func(t *testing.T) []models.EORecord {
				// The sub-referral is OLDER than its parent in the child
				// order. Nothing forbids it, and a one-pass implementation
				// that attaches as it goes would lose the link.
				r1 := eoRefTreeBranch(t, "IN-2026-0001-R1", false, nil, nil)
				r2 := eoRefTreeBranch(t, "IN-2026-0001-R2", false, &r1, nil)
				return []models.EORecord{r2, r1}
			},
			want: []eoRefTreeWant{
				{reference: "IN-2026-0001-R1", depth: 0},
				{reference: "IN-2026-0001-R2", parent: "IN-2026-0001-R1", depth: 1},
			},
		},
		{
			name: "a branch whose parent is not a branch of this letter is consolidated and SAID",
			build: func(t *testing.T) []models.EORecord {
				stranger := eoRefTreeRec(t, "IN-2026-0099-R1", nil)
				return []models.EORecord{
					eoRefTreeBranch(t, "IN-2026-0001-R1", false, &stranger, nil),
				}
			},
			want: []eoRefTreeWant{
				{reference: "IN-2026-0001-R1", depth: 0, anomaly: "parent_outside_tree"},
			},
		},
		{
			name: "a parent value that is not a record id is consolidated and SAID",
			build: func(t *testing.T) []models.EORecord {
				return []models.EORecord{
					eoRefTreeBranch(t, "IN-2026-0001-R1", false, nil, map[string]any{"parent_referral": "IN-2026-0001-R2"}),
					eoRefTreeBranch(t, "IN-2026-0001-R2", false, nil, map[string]any{"parent_referral": 7}),
				}
			},
			want: []eoRefTreeWant{
				{reference: "IN-2026-0001-R1", depth: 0, anomaly: "parent_unreadable"},
				{reference: "IN-2026-0001-R2", depth: 0, anomaly: "parent_unreadable"},
			},
		},
		{
			name: "a blank or absent parent is an ordinary top-level branch, not an anomaly",
			build: func(t *testing.T) []models.EORecord {
				return []models.EORecord{
					eoRefTreeBranch(t, "IN-2026-0001-R1", false, nil, map[string]any{"parent_referral": "   "}),
					eoRefTreeBranch(t, "IN-2026-0001-R2", false, nil, map[string]any{"parent_referral": nil}),
					eoRefTreeBranch(t, "IN-2026-0001-R3", false, nil, nil),
				}
			},
			want: []eoRefTreeWant{
				{reference: "IN-2026-0001-R1", depth: 0},
				{reference: "IN-2026-0001-R2", depth: 0},
				{reference: "IN-2026-0001-R3", depth: 0},
			},
		},
		{
			name: "a branch that names itself is consolidated once, at the root, and SAID",
			build: func(t *testing.T) []models.EORecord {
				r1 := eoRefTreeRec(t, "IN-2026-0001-R1", nil)
				r1.Values = datatypes.JSON(`{"parent_referral":"` + r1.ID.String() + `"}`)
				return []models.EORecord{r1}
			},
			want: []eoRefTreeWant{
				{reference: "IN-2026-0001-R1", depth: 0, anomaly: "parent_cycle"},
			},
		},
		{
			name: "two branches that name each other are both consolidated, and the cycle is SAID",
			build: func(t *testing.T) []models.EORecord {
				r1 := eoRefTreeRec(t, "IN-2026-0001-R1", nil)
				r2 := eoRefTreeRec(t, "IN-2026-0001-R2", nil)
				r1.Values = datatypes.JSON(`{"parent_referral":"` + r2.ID.String() + `"}`)
				r2.Values = datatypes.JSON(`{"parent_referral":"` + r1.ID.String() + `"}`)
				return []models.EORecord{r1, r2}
			},
			want: []eoRefTreeWant{
				{reference: "IN-2026-0001-R1", depth: 0, anomaly: "parent_cycle"},
				{reference: "IN-2026-0001-R2", parent: "IN-2026-0001-R1", depth: 1},
			},
		},
		{
			name:  "a letter with no branches at all consolidates to an empty tree",
			build: func(t *testing.T) []models.EORecord { return nil },
			want:  []eoRefTreeWant{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := eoRefTreeLetter(t, map[string]any{"real_ask": "the water tariff"})
			branches := tc.build(t)
			read := eoRefTreeReaderFor(rec, branches)

			view := eoRefTreeStored(t, rec, eoRefTreeDerive(t, capability, read, rec, cfg), eoRefTreeTargetKey)
			if len(view.Branches) != len(branches) {
				t.Fatalf("consolidated %d branches of %d; a branch the platform omits makes the output look complete when it is not\n%+v",
					len(view.Branches), len(branches), view.Branches)
			}
			if got := eoRefTreeFlatten(t, view, branches); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("tree =\n  %+v\nwant\n  %+v", got, tc.want)
			}
			if want := []string{eoRefTreeBranchTypeKey}; !reflect.DeepEqual(read.asked, want) {
				t.Errorf("read %v, want exactly one read of %v — the tree is built from ONE flat read of the letter's children, never a walk per node", read.asked, want)
			}
		})
	}

	// The ancestor bound exists because the chain is untrusted input: it is
	// clerk-entered values, not a column the database constrains. A chain
	// past the bound must still consolidate every branch.
	//
	// "Exactly one branch is flagged" is pinned to THIS fixture's build
	// order, not asserted as a property: cutting a branch detaches the chain
	// above it, so in another order a different branch — or a different
	// NUMBER of them — carries the flag. What holds in every order is the
	// count assertion above it: every branch appears exactly once.
	t.Run("a chain deeper than the ancestor bound is cut and SAID, and every branch is still consolidated", func(t *testing.T) {
		rec := eoRefTreeLetter(t, nil)
		branches := make([]models.EORecord, 0, eoMaxAncestorDepth+2)
		for i := 0; i <= eoMaxAncestorDepth+1; i++ {
			var parent *models.EORecord
			if i > 0 {
				parent = &branches[i-1]
			}
			branches = append(branches, eoRefTreeBranch(t, fmt.Sprintf("IN-2026-0001-R%d", i+1), false, parent, nil))
		}
		read := eoRefTreeReaderFor(rec, branches)
		view := eoRefTreeStored(t, rec, eoRefTreeDerive(t, capability, read, rec, cfg), eoRefTreeTargetKey)
		if len(view.Branches) != len(branches) {
			t.Fatalf("consolidated %d branches of %d", len(view.Branches), len(branches))
		}
		cut := 0
		for _, b := range view.Branches {
			if b.Anomaly == "parent_too_deep" {
				cut++
			}
		}
		if cut != 1 {
			t.Errorf("%d branches flagged parent_too_deep, want exactly the one at the bound", cut)
		}
	})

	t.Run("a branch type that names no record type is an error, not an empty tree", func(t *testing.T) {
		rec := eoRefTreeLetter(t, nil)
		read := eoRefTreeReaderFor(rec, nil)
		_, err := capability.Derive(context.Background(), read, rec,
			eoRefTreeConfig(t, map[string]any{"branch_type_key": "eo_not_a_type"}))
		if err == nil {
			t.Fatal("want an error naming the missing branch type; a typo must not look like a letter with no referrals")
		}
	})

	t.Run("the branch type and the parent key are configuration, not constants", func(t *testing.T) {
		rec := eoRefTreeLetter(t, nil)
		a := eoRefTreeRec(t, "SUB-1", nil)
		b := eoRefTreeRec(t, "SUB-2", map[string]any{"branched_from": a.ID.String()})
		read := &eoRefTreeReader{children: map[uuid.UUID]map[string][]models.EORecord{
			rec.ID: {"eo_branch": {a, b}},
		}}
		got := eoRefTreeDerive(t, capability, read, rec, eoRefTreeConfig(t, map[string]any{
			"branch_type_key": "eo_branch",
			"parent_key":      "branched_from",
			"target_key":      "eo_branch_tree",
		}))
		view := eoRefTreeStored(t, rec, got, "eo_branch_tree")
		want := []eoRefTreeWant{
			{reference: "SUB-1", depth: 0},
			{reference: "SUB-2", parent: "SUB-1", depth: 1},
		}
		if flat := eoRefTreeFlatten(t, view, []models.EORecord{a, b}); !reflect.DeepEqual(flat, want) {
			t.Errorf("tree = %+v, want %+v", flat, want)
		}
	})
}

// ── outstanding branches ────────────────────────────────────────────────────

// TestReferralTreeReportsOutstandingBranches: a branch has answered when the
// engine closed it. That is the SAME signal BlockingChildren reads
// (ClosedAt == nil), so "this branch is outstanding" and "this branch holds
// the letter open" cannot drift apart. The report never refuses anything: the
// refusal is the transition pipeline's, over live rows.
func TestReferralTreeReportsOutstandingBranches(t *testing.T) {
	capability := NewEOReferralTreeCapability()
	cfg := eoRefTreeStdConfig(t)

	for _, tc := range []struct {
		name            string
		build           func(t *testing.T) []models.EORecord
		wantOutstanding []string
		wantComplete    bool
	}{
		{
			name: "every branch has answered",
			build: func(t *testing.T) []models.EORecord {
				return []models.EORecord{
					eoRefTreeBranch(t, "IN-2026-0001-R1", true, nil, nil),
					eoRefTreeBranch(t, "IN-2026-0001-R2", true, nil, nil),
				}
			},
			wantOutstanding: []string{},
			wantComplete:    true,
		},
		{
			name: "one branch has not, and it is named",
			build: func(t *testing.T) []models.EORecord {
				return []models.EORecord{
					eoRefTreeBranch(t, "IN-2026-0001-R1", true, nil, nil),
					eoRefTreeBranch(t, "IN-2026-0001-R2", false, nil, nil),
				}
			},
			wantOutstanding: []string{"IN-2026-0001-R2"},
			wantComplete:    false,
		},
		{
			name: "an OUTSTANDING SUB-referral under an answered parent still holds the letter",
			build: func(t *testing.T) []models.EORecord {
				r1 := eoRefTreeBranch(t, "IN-2026-0001-R1", true, nil, nil)
				r2 := eoRefTreeBranch(t, "IN-2026-0001-R2", false, &r1, nil)
				return []models.EORecord{r1, r2}
			},
			wantOutstanding: []string{"IN-2026-0001-R2"},
			wantComplete:    false,
		},
		{
			name: "the outstanding list is in tree order, not child order",
			build: func(t *testing.T) []models.EORecord {
				r1 := eoRefTreeBranch(t, "IN-2026-0001-R1", true, nil, nil)
				r2 := eoRefTreeBranch(t, "IN-2026-0001-R2", false, nil, nil)
				r3 := eoRefTreeBranch(t, "IN-2026-0001-R3", false, &r1, nil)
				return []models.EORecord{r1, r2, r3}
			},
			wantOutstanding: []string{"IN-2026-0001-R3", "IN-2026-0001-R2"},
			wantComplete:    false,
		},
		{
			// Stated rather than hidden: with no branches the report is
			// vacuously complete. The letter's closure is NOT guarded by
			// this value — BlockingChildren guards it, over live rows — so a
			// consumer that means "ready to draft" must read the branch list
			// too.
			name:            "a letter with no branches is vacuously complete, and the empty list says so",
			build:           func(t *testing.T) []models.EORecord { return nil },
			wantOutstanding: []string{},
			wantComplete:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := eoRefTreeLetter(t, nil)
			branches := tc.build(t)
			read := eoRefTreeReaderFor(rec, branches)
			view := eoRefTreeStored(t, rec, eoRefTreeDerive(t, capability, read, rec, cfg), eoRefTreeTargetKey)

			if !reflect.DeepEqual(view.Outstanding, tc.wantOutstanding) {
				t.Errorf("outstanding = %v, want %v", view.Outstanding, tc.wantOutstanding)
			}
			if view.Complete != tc.wantComplete {
				t.Errorf("complete = %v, want %v", view.Complete, tc.wantComplete)
			}
			if len(view.Branches) != len(branches) {
				t.Errorf("consolidated %d branches of %d", len(view.Branches), len(branches))
			}
		})
	}

	t.Run("a tree that no longer matches is replaced, not merged", func(t *testing.T) {
		rec := eoRefTreeLetter(t, nil)
		// An earlier consolidation is stored, naming a branch that has since
		// been removed from the letter.
		rec.Values = datatypes.JSON(`{"referral_tree":{"branches":[{"id":"` + uuid.New().String() +
			`","reference":"IN-2026-0001-R9","parent_id":"","depth":0,"answered":false}],"outstanding":["IN-2026-0001-R9"],"complete":false}}`)
		read := eoRefTreeReaderFor(rec, nil)
		view := eoRefTreeStored(t, rec, eoRefTreeDerive(t, capability, read, rec, cfg), eoRefTreeTargetKey)
		if len(view.Branches) != 0 || len(view.Outstanding) != 0 || !view.Complete {
			t.Errorf("stored tree = %+v, want the stale branch gone", view)
		}
	})
}

// ── the deadline is not this capability's to write ──────────────────────────

// TestReferralTreeDoesNotDuplicateDeadlineDerivation. A Referral's deadline is
// the link type's rule (services.DeriveDeadlineFromParent, armed by Task A4's
// eo_referral_parent), and the spec lists it in TWO places — §7.5 as
// capability-backed and §7.6 as a link rule. Two mechanisms writing one column
// is the failure this test exists to prevent, so it proves the capability does
// not write it BY DIFFING rather than by naming the key.
func TestReferralTreeDoesNotDuplicateDeadlineDerivation(t *testing.T) {
	ctx := context.Background()
	capability := NewEOReferralTreeCapability()
	cfg := eoRefTreeStdConfig(t)

	t.Run("every hook leaves every value and every column but the one key it owns exactly as it was", func(t *testing.T) {
		rec := eoRefTreeLetter(t, map[string]any{
			"real_ask":     "the water tariff",
			"consolidated": "the Office's own answer, already written",
			"referral_tree": map[string]any{
				"branches": []any{}, "outstanding": []any{}, "complete": true,
			},
		})
		letterDue := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
		rec.DueDate = &letterDue
		was := eoDecodeValues(rec.Values)
		wasDue := *rec.DueDate

		branchDue := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
		branch := eoRefTreeBranch(t, "IN-2026-0001-R1", false, nil, nil)
		branch.DueDate = &branchDue
		read := eoRefTreeReaderFor(rec, []models.EORecord{branch})

		if _, err := capability.Validate(ctx, read, rec, cfg); err != nil {
			t.Fatalf("validate: %v", err)
		}
		if err := capability.OnTransition(ctx, read, rec, &models.WorkflowTransition{Code: "consolidate"}, cfg); err != nil {
			t.Fatalf("on transition: %v", err)
		}
		if _, err := capability.Timers(ctx, read, rec, cfg); err != nil {
			t.Fatalf("timers: %v", err)
		}
		derived := eoRefTreeDerive(t, capability, read, rec, cfg)
		if errs := eoApplyValues(rec, derived); len(errs) > 0 {
			t.Fatalf("apply derived values: %v", errs)
		}

		owned, err := capability.ReservedKeys(cfg)
		if err != nil {
			t.Fatalf("reserved keys: %v", err)
		}
		isOwned := map[string]bool{}
		for _, k := range owned {
			isOwned[k] = true
		}
		now := eoDecodeValues(rec.Values)
		for k, v := range now {
			if isOwned[k] {
				continue
			}
			if !reflect.DeepEqual(was[k], v) {
				t.Errorf("the capability changed %q from %#v to %#v; it owns only %v", k, was[k], v, owned)
			}
		}
		for k, v := range was {
			if _, still := now[k]; !still && !isOwned[k] {
				t.Errorf("the capability removed %q (%#v)", k, v)
			}
		}
		// The deadline COLUMN, which is where a Referral's deadline actually
		// lives (eoMatrixKey maps `deadline` onto due_date).
		if rec.DueDate == nil || !rec.DueDate.Equal(wasDue) {
			t.Errorf("the letter's due_date is now %v, want %v; the deadline is the link type's rule and no capability's", rec.DueDate, wasDue)
		}
		if branch.DueDate == nil || !branch.DueDate.Equal(branchDue) {
			t.Errorf("the branch's due_date is now %v, want %v", branch.DueDate, branchDue)
		}
	})

	t.Run("Derive returns the one key the capability owns and no other", func(t *testing.T) {
		rec := eoRefTreeLetter(t, nil)
		read := eoRefTreeReaderFor(rec, []models.EORecord{eoRefTreeBranch(t, "IN-2026-0001-R1", true, nil, nil)})
		got := eoRefTreeDerive(t, capability, read, rec, cfg)
		if len(got) != 1 {
			t.Fatalf("Derive wrote %d keys, want exactly one", len(got))
		}
		if _, ok := got[eoRefTreeTargetKey]; !ok {
			t.Errorf("Derive wrote %v, want [%s]", eoRefTreeKeysOf(got), eoRefTreeTargetKey)
		}
	})

	// A config is the other way a capability could come to own the column:
	// the target key is configurable, so it is refused by name.
	t.Run("no configuration can aim this capability at the deadline", func(t *testing.T) {
		rec := eoRefTreeLetter(t, nil)
		read := eoRefTreeReaderFor(rec, nil)
		for _, target := range []string{"due_date", "deadline", "closed_at", "state_id", "owning_body_id", "sensitivity"} {
			c := eoRefTreeConfig(t, map[string]any{"branch_type_key": eoRefTreeBranchTypeKey, "target_key": target})
			if _, err := capability.Derive(ctx, read, rec, c); err == nil {
				t.Errorf("target_key %q was accepted; the link rule owns the deadline", target)
			}
			if _, err := capability.ReservedKeys(c); err == nil {
				t.Errorf("ReservedKeys accepted target_key %q", target)
			}
		}
	})

	// A source-level test, in the shape of the one in
	// eo_capability_decisions_base_test.go. The behavioural tests above can
	// only catch a write on the paths they drive; this catches the key being
	// named at all — including on a path nothing yet calls.
	t.Run("the capability's source names the deadline and the officer's prose only to refuse them", func(t *testing.T) {
		const src = "eo_capability_referral_tree.go"
		const denyList = "eoReferralTreeForbiddenTargets"
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, src, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", src, err)
		}
		refusals := map[token.Pos]bool{}
		var denied []string
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, name := range spec.Names {
				if name.Name != denyList || i >= len(spec.Values) {
					continue
				}
				ast.Inspect(spec.Values[i], func(n ast.Node) bool {
					if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						refusals[lit.Pos()] = true
						denied = append(denied, lit.Value)
					}
					return true
				})
			}
			return true
		})
		// `due_date` is deliberately NOT here: the column is refused
		// structurally against eoRecordColumnWriters, so the file names it
		// nowhere at all, and the sweep below is what holds that.
		for _, want := range []string{`"deadline"`, `"consolidated"`, `"reply_text"`} {
			if !eoRefTreeHas(denied, want) {
				t.Errorf("%s = %v; %s must be refused by name, not left to another file's field definitions", denyList, denied, want)
			}
		}
		// The words that must appear in no other string literal of the file:
		// the matrix's spelling of the deadline the link rule owns, the
		// column it maps onto, and the officer's own prose field the client
		// reserved for a person.
		for _, forbidden := range []string{"due_date", "deadline", "consolidat"} {
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING || refusals[lit.Pos()] {
					return true
				}
				if strings.Contains(strings.ToLower(lit.Value), forbidden) {
					t.Errorf("%s names %s in code at %s, outside %s; the deadline belongs to the link type and the one answer belongs to the officer",
						src, lit.Value, fset.Position(lit.Pos()), denyList)
				}
				return true
			})
		}
	})

	t.Run("this capability arms no timer", func(t *testing.T) {
		rec := eoRefTreeLetter(t, nil)
		read := eoRefTreeReaderFor(rec, []models.EORecord{eoRefTreeBranch(t, "IN-2026-0001-R1", false, nil, nil)})
		timers, err := capability.Timers(ctx, read, rec, cfg)
		if err != nil {
			t.Fatalf("timers: %v", err)
		}
		if len(timers) != 0 {
			t.Errorf("Timers armed %d; each branch's own deadline already arms the SLA clock, and a second clock on the letter would escalate the same lateness twice", len(timers))
		}
	})
}

// ── the visibility decision (addendum item 5) ───────────────────────────────

// TestReferralTreeCarriesNoBranchContentAcrossBodies pins the product decision
// this task had to make. The capability reads EVERY branch — Children applies
// no row-level visibility, and the issuing Office is entitled to all of them
// (the creator exemption in repository.applyEORecordVisibility was written for
// exactly this) — so no branch is ever omitted. What the consolidated value
// carries is therefore the other half of the decision: structure and status,
// never a branch's content, because the letter is unrestricted and a reader
// entitled to the letter is NOT thereby entitled to a branch.
func TestReferralTreeCarriesNoBranchContentAcrossBodies(t *testing.T) {
	capability := NewEOReferralTreeCapability()
	cfg := eoRefTreeStdConfig(t)

	// Every value a branch of the pilot's Referral type carries, each with a
	// marker a test can look for in the stored output.
	legalBody := uuid.New().String()
	content := map[string]any{
		"referred_body":  legalBody,
		"referral_owner": uuid.New().String(),
		"ask_type":       "position",
		"instruction":    "eo-marker-instruction",
		"response":       "eo-marker-response-legal-flags-the-zoning-bylaw",
		"response_files": []string{"eo-marker-file"},
		"visible_to":     legalBody,
	}

	rec := eoRefTreeLetter(t, nil)
	restricted := eoRefTreeBranch(t, "IN-2026-0001-R1", true, nil, content)
	open := eoRefTreeBranch(t, "IN-2026-0001-R2", false, nil, nil)
	read := eoRefTreeReaderFor(rec, []models.EORecord{restricted, open})

	derived := eoRefTreeDerive(t, capability, read, rec, cfg)
	// Decoding strictly into the declared view is the structural half of the
	// guarantee: a branch's answer could only be carried in a field the view
	// does not declare, and that fails to decode.
	view := eoRefTreeStored(t, rec, derived, eoRefTreeTargetKey)

	t.Run("the restricted branch is consolidated, not quietly dropped", func(t *testing.T) {
		if len(view.Branches) != 2 {
			t.Fatalf("consolidated %d branches of 2; a consolidation that silently omits a body is worse than one that refuses", len(view.Branches))
		}
		if view.Branches[0].Reference != "IN-2026-0001-R1" || !view.Branches[0].Answered {
			t.Errorf("branch 0 = %+v, want the restricted branch, answered", view.Branches[0])
		}
	})

	t.Run("no value of a branch reaches the letter", func(t *testing.T) {
		raw, err := json.Marshal(derived)
		if err != nil {
			t.Fatalf("encode derived: %v", err)
		}
		// Every marker is searched for, whatever SHAPE the branch stored it
		// in. Skipping non-strings would have left a list-valued field
		// (`response_files`) unsearched, so an answer lifted out of a list
		// and stuffed into a field the declared shape DOES have — Anomaly,
		// say — would have passed both halves of this guarantee.
		searched := map[string]bool{}
		for key, value := range content {
			for _, marker := range eoRefTreeMarkers(t, value) {
				searched[marker] = true
				if strings.Contains(string(raw), marker) {
					t.Errorf("the consolidated value carries the branch's %q (%s). The letter carries no visible_to, so storing a restricted branch's content on it routes §7.11 around its own control", key, marker)
				}
			}
		}
		// The search itself is worth an assertion: a marker that is not
		// looked for is a leak nobody would notice, and the element inside
		// the list is the one a shape-blind search drops.
		for _, want := range []string{"eo-marker-file", "eo-marker-response-legal-flags-the-zoning-bylaw", legalBody} {
			if !searched[want] {
				t.Errorf("%q was never searched for; a value this test does not look at is a value it does not protect", want)
			}
		}
	})
}

// ── configuration ───────────────────────────────────────────────────────────

// TestReferralTreeConfigIsStrict: a stray or missing key makes the record type
// unusable at the first request against it, not at seed time, so every hook
// that could act on a config parses it first and refuses rather than quietly
// consolidating nothing.
func TestReferralTreeConfigIsStrict(t *testing.T) {
	ctx := context.Background()
	capability := NewEOReferralTreeCapability()

	for _, tc := range []struct {
		name string
		cfg  string
	}{
		{"no config at all", ``},
		{"an empty object", `{}`},
		{"json null", `null`},
		{"an unknown key", `{"branch_type_key":"eo_referral","branch_type":"oops"}`},
		{"a key of the right shape but the wrong name", `{"branch_type_keys":["eo_referral"]}`},
		{"no branch type, so it would read nothing", `{"parent_key":"parent_referral"}`},
		{"a blank branch type", `{"branch_type_key":"   "}`},
		{"a blank parent key, so the tree would have no shape", `{"branch_type_key":"eo_referral","parent_key":"   "}`},
		{"a blank target key, so the tree would have nowhere to go", `{"branch_type_key":"eo_referral","target_key":"  "}`},
		{"the target key is also the parent key the clerk fills", `{"branch_type_key":"eo_referral","parent_key":"parent_referral","target_key":"parent_referral"}`},
		{"the target key is the deadline the link rule owns", `{"branch_type_key":"eo_referral","target_key":"deadline"}`},
		{"the target key is the column that deadline maps onto", `{"branch_type_key":"eo_referral","target_key":"due_date"}`},
		{"the target key is a key the engine owns outright", `{"branch_type_key":"eo_referral","target_key":"reference"}`},
		{"the target key is the officer's own prose", `{"branch_type_key":"eo_referral","target_key":"consolidated"}`},
		{"a malformed blob", `{"branch_type_key":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := datatypes.JSON(tc.cfg)
			rec := eoRefTreeLetter(t, nil)
			read := eoRefTreeReaderFor(rec, nil)
			if _, err := capability.Derive(ctx, read, rec, cfg); err == nil {
				t.Error("Derive accepted it")
			}
			if _, err := capability.Validate(ctx, read, rec, cfg); err == nil {
				t.Error("Validate accepted it")
			}
			if err := capability.OnTransition(ctx, read, rec, &models.WorkflowTransition{Code: "consolidate"}, cfg); err == nil {
				t.Error("OnTransition accepted it")
			}
			if _, err := capability.Timers(ctx, read, rec, cfg); err == nil {
				t.Error("Timers accepted it")
			}
			if _, err := capability.ReservedKeys(cfg); err == nil {
				t.Error("ReservedKeys accepted it, so the reserved key would go undeclared")
			}
			if len(read.asked) != 0 {
				t.Errorf("a misconfigured capability read the letter's children anyway: %v", read.asked)
			}
		})
	}
}

// ── the reserved key ────────────────────────────────────────────────────────

// TestReferralTreeReservesItsTargetKey: the consolidated tree is the engine's
// account of what the platform found, so no request may write it. ReservedKeys
// is the whole of that guard (eoCapabilitiesCheckConfig), which is why this
// capability does not embed eoReservesNoKeys.
func TestReferralTreeReservesItsTargetKey(t *testing.T) {
	capability := NewEOReferralTreeCapability()

	t.Run("the default target, and a configured one", func(t *testing.T) {
		for cfg, want := range map[string]string{
			`{"branch_type_key":"eo_referral"}`:                            eoRefTreeTargetKey,
			`{"branch_type_key":"eo_referral","target_key":"eo_branches"}`: "eo_branches",
		} {
			got, err := capability.ReservedKeys(datatypes.JSON(cfg))
			if err != nil {
				t.Fatalf("reserved keys: %v", err)
			}
			if !reflect.DeepEqual(got, []string{want}) {
				t.Errorf("ReservedKeys(%s) = %v, want [%s]", cfg, got, want)
			}
		}
	})

	enabled := []EOEnabledCapability{{Capability: capability, Config: eoRefTreeStdConfig(t)}}

	t.Run("a transition field change may not write it", func(t *testing.T) {
		tr := &models.WorkflowTransition{Code: "consolidate", FieldChanges: []models.TransitionFieldChange{{FieldName: eoRefTreeTargetKey}}}
		err := eoCapabilitiesCheckConfig(enabled, nil, &models.Workflow{Code: "wf"}, tr)
		if !errors.Is(err, ErrEOCapabilityConfig) {
			t.Errorf("a transition that writes %s was accepted: %v", eoRefTreeTargetKey, err)
		}
	})

	// The rule whichever task enables this capability must honour: the type
	// may not ALSO declare the key as a field. No pilot type declares
	// `referral_tree` today, so unlike decisions_base's `related_refs` this
	// one does not collide with the seed — but the rule is stated here rather
	// than left to that happy accident.
	t.Run("a field definition may not declare it", func(t *testing.T) {
		fields := []models.EOFieldDef{{Key: eoRefTreeTargetKey}}
		err := eoCapabilitiesCheckConfig(enabled, fields, nil, nil)
		if !errors.Is(err, ErrEOCapabilityConfig) {
			t.Errorf("a type declaring %s was accepted: %v", eoRefTreeTargetKey, err)
		}
	})

	t.Run("the config this capability names nothing of is the workflow", func(t *testing.T) {
		if err := capability.ValidateWorkflow(eoRefTreeStdConfig(t), &models.Workflow{Code: "wf"}); err != nil {
			t.Errorf("ValidateWorkflow: %v", err)
		}
	})
}

// ── wiring ──────────────────────────────────────────────────────────────────

// TestReferralTreeIsRegisteredByTheWiringCall: a capability a record type can
// name but nothing registered is an error at the first request, and a
// capability registered nowhere is one that never runs.
func TestReferralTreeIsRegisteredByTheWiringCall(t *testing.T) {
	saved := eoDefaultCapabilities
	eoDefaultCapabilities = newEOCapabilityRegistry()
	defer func() { eoDefaultCapabilities = saved }()
	RegisterEOCapabilities()

	typ := eoCapType(t, []map[string]any{{"key": EOReferralTreeKey, "config": map[string]any{
		"branch_type_key": eoRefTreeBranchTypeKey,
	}}})
	typ.Key = "eo_incoming_correspondence"
	got, err := CapabilitiesFor(typ)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 1 || got[0].Key() != "referral_tree" {
		t.Fatalf("resolved %+v, want the referral_tree capability", got)
	}
}

// ── small helpers ───────────────────────────────────────────────────────────

// eoRefTreeMarkers returns every string that appears anywhere inside v, plus
// v's own JSON encoding. A leak is caught whether it copies the whole value or
// lifts one element out of a list.
func eoRefTreeMarkers(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode a branch value: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode a branch value: %v", err)
	}
	out := []string{string(raw)}
	var walk func(any)
	walk = func(n any) {
		switch x := n.(type) {
		case string:
			if strings.TrimSpace(x) != "" {
				out = append(out, x)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(decoded)
	return out
}

func eoRefTreeHas(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

func eoRefTreeKeysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
