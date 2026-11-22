package services

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

func eoLinkTypeIDs(t *testing.T, v any) datatypes.JSON {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return datatypes.JSON(b)
}

func eoLinkRec(typeID uuid.UUID) *models.EORecord {
	return &models.EORecord{ID: uuid.New(), RecordTypeID: typeID}
}

func TestValidateLinkRejectsWrongTargetType(t *testing.T) {
	srcT, okT, badT := uuid.New(), uuid.New(), uuid.New()
	lt := &models.EOLinkType{
		Key:           "covers_item",
		SourceTypeIDs: eoLinkTypeIDs(t, []string{srcT.String()}),
		TargetTypeIDs: eoLinkTypeIDs(t, []string{okT.String()}),
		Cardinality:   models.EOCardinalityMany,
	}
	cases := []struct {
		name     string
		from, to *models.EORecord
		wantErr  bool
	}{
		{"valid pair", eoLinkRec(srcT), eoLinkRec(okT), false},
		{"wrong target", eoLinkRec(srcT), eoLinkRec(badT), true},
		{"wrong source", eoLinkRec(badT), eoLinkRec(okT), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateLink(lt, c.from, c.to, nil)
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestValidateLinkEnforcesCardinality(t *testing.T) {
	typ := uuid.New()
	ids := eoLinkTypeIDs(t, []string{typ.String()})
	from, to1, to2 := eoLinkRec(typ), eoLinkRec(typ), eoLinkRec(typ)
	link := func(key string, f, to *models.EORecord) models.EORecordLink {
		return models.EORecordLink{FromRecordID: f.ID, ToRecordID: to.ID, LinkTypeKey: key}
	}
	cases := []struct {
		name     string
		card     string
		existing []models.EORecordLink
		to       *models.EORecord
		wantErr  bool
	}{
		{"one, none yet", models.EOCardinalityOne, nil, to1, false},
		{"one, already has one", models.EOCardinalityOne, []models.EORecordLink{link("k", from, to1)}, to2, true},
		{"one, other type's link ignored", models.EOCardinalityOne, []models.EORecordLink{link("other", from, to1)}, to2, false},
		{"one, other source's link ignored", models.EOCardinalityOne, []models.EORecordLink{link("k", to1, to2)}, to2, false},
		{"many, already has one", models.EOCardinalityMany, []models.EORecordLink{link("k", from, to1)}, to2, false},
		{"many, exact duplicate", models.EOCardinalityMany, []models.EORecordLink{link("k", from, to1)}, to1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lt := &models.EOLinkType{Key: "k", SourceTypeIDs: ids, TargetTypeIDs: ids, Cardinality: c.card}
			err := ValidateLink(lt, from, c.to, c.existing)
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestValidateLinkAllowsPolymorphicAnyTarget(t *testing.T) {
	taskT := uuid.New()
	lt := &models.EOLinkType{
		Key:           "parent_record",
		SourceTypeIDs: eoLinkTypeIDs(t, []string{taskT.String()}),
		TargetTypeIDs: eoLinkTypeIDs(t, "any"),
		Cardinality:   models.EOCardinalityOne,
	}
	for _, name := range []string{"type a", "type b", "type c"} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateLink(lt, eoLinkRec(taskT), eoLinkRec(uuid.New()), nil); err != nil {
				t.Errorf("any target rejected: %v", err)
			}
		})
	}
	t.Run("any does not loosen the source side", func(t *testing.T) {
		if err := ValidateLink(lt, eoLinkRec(uuid.New()), eoLinkRec(uuid.New()), nil); err == nil {
			t.Error("non-Task source accepted")
		}
	})
	t.Run("any inside an array", func(t *testing.T) {
		lt2 := *lt
		lt2.TargetTypeIDs = eoLinkTypeIDs(t, []string{"any"})
		if err := ValidateLink(&lt2, eoLinkRec(taskT), eoLinkRec(uuid.New()), nil); err != nil {
			t.Errorf("[\"any\"] rejected: %v", err)
		}
	})
	t.Run("empty list permits nothing", func(t *testing.T) {
		lt2 := *lt
		lt2.TargetTypeIDs = eoLinkTypeIDs(t, []string{})
		if err := ValidateLink(&lt2, eoLinkRec(taskT), eoLinkRec(uuid.New()), nil); err == nil {
			t.Error("empty target list accepted a link")
		}
	})
}

func TestDeriveDeadlineFromParentOffset(t *testing.T) {
	due := time.Date(2026, 12, 31, 9, 0, 0, 0, time.UTC)
	parent := &models.EORecord{DueDate: &due}
	cases := []struct {
		name   string
		lt     *models.EOLinkType
		parent *models.EORecord
		want   *time.Time
	}{
		{"offset counted back", &models.EOLinkType{DeriveDeadlineFromParent: true, DeadlineOffsetDays: 7}, parent, eoTimePtr(2026, 12, 24)},
		{"zero offset", &models.EOLinkType{DeriveDeadlineFromParent: true}, parent, eoTimePtr(2026, 12, 31)},
		{"crosses a year", &models.EOLinkType{DeriveDeadlineFromParent: true, DeadlineOffsetDays: 40}, parent, eoTimePtr(2026, 11, 21)},
		{"rule off", &models.EOLinkType{DeriveDeadlineFromParent: false, DeadlineOffsetDays: 7}, parent, nil},
		{"parent has no due date", &models.EOLinkType{DeriveDeadlineFromParent: true, DeadlineOffsetDays: 7}, &models.EORecord{}, nil},
		{"nil parent", &models.EOLinkType{DeriveDeadlineFromParent: true}, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DeriveDeadlineFromParent(c.lt, c.parent)
			switch {
			case c.want == nil && got != nil:
				t.Errorf("got %v, want nil", got)
			case c.want != nil && got == nil:
				t.Errorf("got nil, want %v", c.want)
			case c.want != nil && !got.Equal(*c.want):
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
	t.Run("does not mutate the parent", func(t *testing.T) {
		before := *parent.DueDate
		DeriveDeadlineFromParent(&models.EOLinkType{DeriveDeadlineFromParent: true, DeadlineOffsetDays: 3}, parent)
		if !parent.DueDate.Equal(before) {
			t.Error("parent due date changed")
		}
	})
}

func eoTimePtr(y int, m time.Month, d int) *time.Time {
	v := time.Date(y, m, d, 9, 0, 0, 0, time.UTC)
	return &v
}

func TestBlockingChildrenClosureVsTransition(t *testing.T) {
	assignment := models.EORecord{ID: uuid.New()}
	objection := models.EORecord{ID: uuid.New()}
	plain := models.EORecord{ID: uuid.New()}
	children := []models.EORecord{assignment, objection, plain}
	links := []models.EORecordLink{
		{FromRecordID: assignment.ID, LinkTypeKey: "assignment_of"},
		{FromRecordID: objection.ID, LinkTypeKey: "objection_to"},
		{FromRecordID: plain.ID, LinkTypeKey: "related"},
	}
	lts := map[string]models.EOLinkType{
		"assignment_of": {Key: "assignment_of", BlocksParentClosure: true},
		"objection_to":  {Key: "objection_to", BlocksParentTransitionKey: "raise_to_he"},
		"related":       {Key: "related"},
	}
	ids := func(rs []models.EORecord) map[uuid.UUID]bool {
		m := map[uuid.UUID]bool{}
		for _, r := range rs {
			m[r.ID] = true
		}
		return m
	}
	cases := []struct {
		name string
		key  string
		want []uuid.UUID
	}{
		{"closure: only the assignment", "", []uuid.UUID{assignment.ID}},
		{"raise: only the objection", "raise_to_he", []uuid.UUID{objection.ID}},
		{"unrelated transition: nothing", "approve", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ids(BlockingChildren(children, links, lts, c.key))
			if len(got) != len(c.want) {
				t.Fatalf("got %d blockers, want %d", len(got), len(c.want))
			}
			for _, id := range c.want {
				if !got[id] {
					t.Errorf("missing expected blocker %s", id)
				}
			}
		})
	}

	t.Run("closed child no longer blocks", func(t *testing.T) {
		now := time.Now()
		closed := assignment
		closed.ClosedAt = &now
		if got := BlockingChildren([]models.EORecord{closed}, links, lts, ""); len(got) != 0 {
			t.Errorf("closed child blocked closure: %d", len(got))
		}
	})
	t.Run("child with two blocking links is returned once", func(t *testing.T) {
		two := append([]models.EORecordLink{}, links...)
		two = append(two, models.EORecordLink{FromRecordID: assignment.ID, LinkTypeKey: "assignment_of"})
		if got := BlockingChildren(children, two, lts, ""); len(got) != 1 {
			t.Errorf("got %d, want 1", len(got))
		}
	})
	t.Run("unknown link type does not block", func(t *testing.T) {
		unk := []models.EORecordLink{{FromRecordID: assignment.ID, LinkTypeKey: "nope"}}
		if got := BlockingChildren(children, unk, lts, ""); len(got) != 0 {
			t.Errorf("got %d, want 0", len(got))
		}
	})
}

func TestWalkAncestorsDetectsCycle(t *testing.T) {
	ctx := context.Background()
	a := &models.EORecord{ID: uuid.New()}
	b := &models.EORecord{ID: uuid.New()}
	a.ParentRecordID = &b.ID
	b.ParentRecordID = &a.ID
	byID := map[uuid.UUID]*models.EORecord{a.ID: a, b.ID: b}
	load := func(id uuid.UUID) (*models.EORecord, error) { return byID[id], nil }

	// a → b → a
	if _, err := WalkAncestors(ctx, a, load, 32); err == nil {
		t.Error("cyclic parent chain did not error")
	}

	t.Run("self parent", func(t *testing.T) {
		s := &models.EORecord{ID: uuid.New()}
		s.ParentRecordID = &s.ID
		if _, err := WalkAncestors(ctx, s, func(uuid.UUID) (*models.EORecord, error) { return s, nil }, 32); err == nil {
			t.Error("self-parent did not error")
		}
	})
}

func TestWalkAncestorsOrderDepthAndErrors(t *testing.T) {
	ctx := context.Background()
	chain := func(n int) ([]*models.EORecord, func(uuid.UUID) (*models.EORecord, error)) {
		recs := make([]*models.EORecord, n)
		byID := map[uuid.UUID]*models.EORecord{}
		for i := range recs {
			recs[i] = &models.EORecord{ID: uuid.New()}
			byID[recs[i].ID] = recs[i]
		}
		for i := 0; i < n-1; i++ {
			recs[i].ParentRecordID = &recs[i+1].ID
		}
		return recs, func(id uuid.UUID) (*models.EORecord, error) { return byID[id], nil }
	}

	t.Run("nearest first, excludes self", func(t *testing.T) {
		recs, load := chain(4)
		got, err := WalkAncestors(ctx, recs[0], load, 32)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got[0].ID != recs[1].ID || got[2].ID != recs[3].ID {
			t.Errorf("unexpected ancestors: %d", len(got))
		}
	})
	t.Run("root has none", func(t *testing.T) {
		recs, load := chain(1)
		got, err := WalkAncestors(ctx, recs[0], load, 32)
		if err != nil || len(got) != 0 {
			t.Errorf("got %d, %v", len(got), err)
		}
	})
	t.Run("depth bound errors", func(t *testing.T) {
		recs, load := chain(10) // 9 ancestors
		if _, err := WalkAncestors(ctx, recs[0], load, 5); err == nil {
			t.Error("chain deeper than maxDepth did not error")
		}
		if _, err := WalkAncestors(ctx, recs[0], load, 9); err != nil {
			t.Errorf("chain exactly at maxDepth errored: %v", err)
		}
	})
	t.Run("missing ancestor errors", func(t *testing.T) {
		recs, _ := chain(2)
		_, err := WalkAncestors(ctx, recs[0], func(uuid.UUID) (*models.EORecord, error) { return nil, nil }, 32)
		if err == nil {
			t.Error("nil ancestor did not error")
		}
	})
	t.Run("loader error propagates", func(t *testing.T) {
		recs, _ := chain(2)
		_, err := WalkAncestors(ctx, recs[0], func(uuid.UUID) (*models.EORecord, error) { return nil, fmt.Errorf("boom") }, 32)
		if err == nil {
			t.Error("loader error swallowed")
		}
	})
	t.Run("cancelled context errors", func(t *testing.T) {
		recs, load := chain(3)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := WalkAncestors(cctx, recs[0], load, 32); err == nil {
			t.Error("cancelled context did not error")
		}
	})
}
