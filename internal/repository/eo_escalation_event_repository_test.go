package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/automax/backend/internal/models"
)

func eoNewEscalationEvent(breachedAt time.Time) *models.EOEscalationEvent {
	return &models.EOEscalationEvent{
		RecordID:     uuid.New(),
		RecordTypeID: uuid.New(),
		StateID:      uuid.New(),
		PolicyID:     uuid.New(),
		StepIndex:    2,
		BreachedAt:   breachedAt,
	}
}

func TestEscalationEventRecordIsIdempotentPerStep(t *testing.T) {
	db := eoMigratedDB(t)
	repo := NewEOEscalationEventRepository(db)
	ctx := context.Background()

	ev := eoNewEscalationEvent(time.Now().Truncate(time.Microsecond))
	created, err := repo.Record(ctx, ev)
	if err != nil || !created {
		t.Fatalf("first Record = (%v, %v), want (true, nil)", created, err)
	}

	again := *ev
	again.ID = uuid.Nil
	created, err = repo.Record(ctx, &again)
	if err != nil || created {
		t.Fatalf("second Record of the same step = (%v, %v), want (false, nil)", created, err)
	}

	// A different step of the same breach is a different event, and so is the
	// same step breached again after the record re-entered the state.
	nextStep := *ev
	nextStep.ID, nextStep.StepIndex = uuid.Nil, ev.StepIndex+1
	reentry := *ev
	reentry.ID, reentry.BreachedAt = uuid.Nil, ev.BreachedAt.Add(time.Hour)
	for name, e := range map[string]*models.EOEscalationEvent{"next step": &nextStep, "re-entry": &reentry} {
		if created, err := repo.Record(ctx, e); err != nil || !created {
			t.Fatalf("Record(%s) = (%v, %v), want (true, nil)", name, created, err)
		}
	}

	got, err := repo.ListForRecord(ctx, ev.RecordID)
	if err != nil {
		t.Fatalf("ListForRecord: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("events for the record = %d, want 3", len(got))
	}
}

func TestCancelForRecordByCapabilityLeavesOtherTimers(t *testing.T) {
	db := eoTimerTestDB(t)
	repo := NewEOTimerRepository(db)
	ctx := context.Background()

	recordID := uuid.New()
	mk := func(key string, rec uuid.UUID) *models.EOTimer {
		tm := eoNewTimer(time.Now().Add(time.Hour))
		tm.RecordID, tm.CapabilityKey = rec, key
		if err := repo.Schedule(ctx, tm); err != nil {
			t.Fatalf("schedule: %v", err)
		}
		return tm
	}
	sla := mk("eo.sla", recordID)
	reminder := mk("reminder", recordID)
	otherRecord := mk("eo.sla", uuid.New())

	if err := repo.CancelForRecordByCapability(ctx, recordID, "eo.sla"); err != nil {
		t.Fatalf("CancelForRecordByCapability: %v", err)
	}
	for _, c := range []struct {
		name string
		tm   *models.EOTimer
		want string
	}{
		{"matching timer", sla, models.EOTimerCancelled},
		{"same record, other capability", reminder, models.EOTimerPending},
		{"same capability, other record", otherRecord, models.EOTimerPending},
	} {
		if got := eoTimerStatus(t, db, c.tm.ID); got != c.want {
			t.Errorf("%s status = %q, want %q", c.name, got, c.want)
		}
	}
}
