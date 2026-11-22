package services

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
)

// eoSLATimers records what ScheduleStateSLA scheduled. Everything else on the
// interface is left to the embedded nil value: a call to it would panic, which
// is the right answer to "the SLA code touched something it should not".
type eoSLATimers struct {
	repository.EOTimerRepository
	scheduled   []models.EOTimer
	cancelled   []string
	scheduleErr error
}

func (f *eoSLATimers) Schedule(_ context.Context, t *models.EOTimer) error {
	if f.scheduleErr != nil {
		return f.scheduleErr
	}
	f.scheduled = append(f.scheduled, *t)
	return nil
}

func (f *eoSLATimers) CancelForRecordByCapability(_ context.Context, _ uuid.UUID, key string) error {
	f.cancelled = append(f.cancelled, key)
	return nil
}

var eoSLAEpoch = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

func eoSLAFreezeClock(t *testing.T) {
	t.Helper()
	prev := eoSLANow
	eoSLANow = func() time.Time { return eoSLAEpoch }
	t.Cleanup(func() { eoSLANow = prev })
}

func eoSLAInt(n int) *int { return &n }

func eoSLARecord() *models.EORecord {
	return &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New()}
}

// eoSLAState builds a state whose SLA is sla in unit, escalating through a
// policy with the given step delays (hours, as the model defines them).
func eoSLAState(sla *int, unit string, delays ...int) *models.WorkflowState {
	policy := &models.EscalationPolicy{ID: uuid.New(), IsActive: true}
	for i, d := range delays {
		policy.Steps = append(policy.Steps, models.EscalationPolicyStep{ID: uuid.New(), PolicyID: policy.ID, StepOrder: i + 1, DelayHours: d})
	}
	return &models.WorkflowState{ID: uuid.New(), SLAHours: sla, SLAUnit: unit, EscalationPolicyID: &policy.ID, EscalationPolicy: policy}
}

func TestScheduleStateSLAConvertsUnits(t *testing.T) {
	eoSLAFreezeClock(t)
	// SLAHours = 3 with SLAUnit = "days" is 72h, not 3h: the field name lies.
	cases := []struct {
		unit string
		want time.Duration
	}{
		{"minutes", 3 * time.Minute},
		{"hours", 3 * time.Hour},
		{"days", 72 * time.Hour},
		{"months", 3 * 30 * 24 * time.Hour},
		{"", 3 * time.Hour}, // the model's default unit is hours
	}
	for _, tc := range cases {
		t.Run(tc.unit, func(t *testing.T) {
			timers := &eoSLATimers{}
			if err := ScheduleStateSLA(context.Background(), eoSLARecord(), eoSLAState(eoSLAInt(3), tc.unit, 0), timers); err != nil {
				t.Fatalf("ScheduleStateSLA: %v", err)
			}
			if len(timers.scheduled) != 1 {
				t.Fatalf("scheduled %d timers, want 1", len(timers.scheduled))
			}
			if got := timers.scheduled[0].FireAt.Sub(eoSLAEpoch); got != tc.want {
				t.Errorf("fires %v after entry, want %v", got, tc.want)
			}
		})
	}
}

// DelayHours really is hours and has no unit: it is added to the SLA as hours
// whatever the state's SLAUnit is. Sub-hour escalation is not expressible.
func TestScheduleStateSLAStepDelayIsHoursNotTheStatesUnit(t *testing.T) {
	eoSLAFreezeClock(t)
	timers := &eoSLATimers{}
	state := eoSLAState(eoSLAInt(3), "days", 24)
	if err := ScheduleStateSLA(context.Background(), eoSLARecord(), state, timers); err != nil {
		t.Fatalf("ScheduleStateSLA: %v", err)
	}
	if got, want := timers.scheduled[0].FireAt.Sub(eoSLAEpoch), (72+24)*time.Hour; got != want {
		t.Errorf("fires %v after entry, want %v (3 days + 24 hours)", got, want)
	}
}

func TestScheduleStateSLATreatsNilAndZeroAsNoSLA(t *testing.T) {
	eoSLAFreezeClock(t)
	for name, sla := range map[string]*int{"nil": nil, "zero": eoSLAInt(0)} {
		for _, unit := range []string{"hours", "days"} {
			t.Run(name+"/"+unit, func(t *testing.T) {
				timers := &eoSLATimers{}
				if err := ScheduleStateSLA(context.Background(), eoSLARecord(), eoSLAState(sla, unit, 0, 24), timers); err != nil {
					t.Fatalf("ScheduleStateSLA: %v", err)
				}
				if len(timers.scheduled) != 0 {
					t.Errorf("scheduled %d timers for a state with no SLA, want 0 (a zero deadline fires at once)", len(timers.scheduled))
				}
			})
		}
	}
}

func TestScheduleStateSLASchedulesNothingWithoutAnActivePolicy(t *testing.T) {
	eoSLAFreezeClock(t)
	inactive := eoSLAState(eoSLAInt(3), "days", 0)
	inactive.EscalationPolicy.IsActive = false
	noPolicy := &models.WorkflowState{ID: uuid.New(), SLAHours: eoSLAInt(3), SLAUnit: "days"}
	noSteps := eoSLAState(eoSLAInt(3), "days")
	for name, st := range map[string]*models.WorkflowState{"inactive policy": inactive, "no policy": noPolicy, "no steps": noSteps} {
		timers := &eoSLATimers{}
		if err := ScheduleStateSLA(context.Background(), eoSLARecord(), st, timers); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(timers.scheduled) != 0 {
			t.Errorf("%s: scheduled %d timers, want 0", name, len(timers.scheduled))
		}
	}
}

// A policy that is referenced but was not loaded must not read as "no
// policy": that would drop the escalation without a word.
func TestScheduleStateSLAErrorsWhenThePolicyWasNotLoaded(t *testing.T) {
	state := eoSLAState(eoSLAInt(3), "days", 0)
	state.EscalationPolicy = nil
	if err := ScheduleStateSLA(context.Background(), eoSLARecord(), state, &eoSLATimers{}); err == nil {
		t.Fatal("ScheduleStateSLA accepted a state whose escalation policy is referenced but not loaded")
	}
}

func TestScheduleStateSLARejectsDuplicateStepOrders(t *testing.T) {
	state := eoSLAState(eoSLAInt(3), "days", 0, 24)
	state.EscalationPolicy.Steps[1].StepOrder = state.EscalationPolicy.Steps[0].StepOrder
	timers := &eoSLATimers{}
	if err := ScheduleStateSLA(context.Background(), eoSLARecord(), state, timers); err == nil {
		t.Fatal("two steps with one step_order were accepted; the second would be dropped as a duplicate event")
	}
	if len(timers.scheduled) != 0 {
		t.Errorf("scheduled %d timers before rejecting, want 0", len(timers.scheduled))
	}
}

func TestScheduleStateSLAPropagatesAScheduleFailure(t *testing.T) {
	timers := &eoSLATimers{scheduleErr: errors.New("db down")}
	if err := ScheduleStateSLA(context.Background(), eoSLARecord(), eoSLAState(eoSLAInt(3), "days", 0), timers); err == nil {
		t.Fatal("a failed Schedule was swallowed; the transition would commit with no SLA clock")
	}
}

func TestEscalationLadderStepsInOrder(t *testing.T) {
	eoSLAFreezeClock(t)
	// EO-DIR: a 3-day SLA, then escalations at 7 and 14 days. DelayHours counts
	// from the breach, so the steps' delays are 0, 4 days and 11 days.
	state := eoSLAState(eoSLAInt(3), "days", 11*24, 0, 4*24)
	// Configured out of order: StepOrder, not slice position, defines the ladder.
	steps := state.EscalationPolicy.Steps
	steps[0].StepOrder, steps[1].StepOrder, steps[2].StepOrder = 3, 1, 2

	timers := &eoSLATimers{}
	rec := eoSLARecord()
	if err := ScheduleStateSLA(context.Background(), rec, state, timers); err != nil {
		t.Fatalf("ScheduleStateSLA: %v", err)
	}
	if len(timers.scheduled) != 3 {
		t.Fatalf("scheduled %d timers, want 3", len(timers.scheduled))
	}
	wantDays := []int{3, 7, 14}
	for i, tm := range timers.scheduled {
		if got, want := tm.FireAt.Sub(eoSLAEpoch), time.Duration(wantDays[i])*24*time.Hour; got != want {
			t.Errorf("step %d fires %v after entry, want %v", i+1, got, want)
		}
		if tm.RecordID != rec.ID || tm.RecordTypeID != rec.RecordTypeID || tm.CapabilityKey != EOEscalationCapabilityKey {
			t.Errorf("step %d timer = %+v, want this record's %q timer", i+1, tm, EOEscalationCapabilityKey)
		}
		var p eoEscalationPayload
		if err := json.Unmarshal(tm.Payload, &p); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if p.StepIndex != i+1 || p.StateID != state.ID || p.PolicyID != state.EscalationPolicy.ID {
			t.Errorf("step %d payload = %+v, want step_index %d of this state and policy", i+1, p, i+1)
		}
	}
}

// ── the handler ─────────────────────────────────────────────────────────────

type eoFakeEventStore struct {
	mu     sync.Mutex
	events []models.EOEscalationEvent
	err    error
}

func (f *eoFakeEventStore) Record(_ context.Context, ev *models.EOEscalationEvent) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	// The real store's unique key.
	for _, e := range f.events {
		if e.RecordID == ev.RecordID && e.StateID == ev.StateID && e.PolicyID == ev.PolicyID &&
			e.StepIndex == ev.StepIndex && e.BreachedAt.Equal(ev.BreachedAt) {
			return false, nil
		}
	}
	f.events = append(f.events, *ev)
	return true, nil
}

func (f *eoFakeEventStore) ListForRecord(context.Context, uuid.UUID) ([]models.EOEscalationEvent, error) {
	return f.events, nil
}

type eoFakeRecordLookup struct {
	repository.EORecordRepository
	rec *models.EORecord
}

func (f *eoFakeRecordLookup) GetAnyByID(context.Context, uuid.UUID) (*models.EORecord, error) {
	return f.rec, nil
}

// eoSLAScheduledFor schedules a state's ladder and returns its timers, as the
// monitor would hand them to the handler.
func eoSLAScheduledFor(t *testing.T, rec *models.EORecord, state *models.WorkflowState) []models.EOTimer {
	t.Helper()
	timers := &eoSLATimers{}
	if err := ScheduleStateSLA(context.Background(), rec, state, timers); err != nil {
		t.Fatalf("ScheduleStateSLA: %v", err)
	}
	return timers.scheduled
}

func TestEscalationEventIsRecordedOncePerStep(t *testing.T) {
	eoSLAFreezeClock(t)
	state := eoSLAState(eoSLAInt(3), "days", 0, 96, 264)
	rec := eoSLARecord()
	rec.StateID = &state.ID
	due := eoSLAScheduledFor(t, rec, state)

	store := &eoFakeEventStore{}
	handle := NewEOEscalationHandler(store, &eoFakeRecordLookup{rec: rec})

	// Re-running the sweep: the monitor delivers at least once, so every timer
	// is handled twice here, and again after a lease expiry.
	for pass := 0; pass < 3; pass++ {
		for _, tm := range due {
			if err := handle(context.Background(), tm); err != nil {
				t.Fatalf("pass %d: %v", pass, err)
			}
		}
	}
	if len(store.events) != 3 {
		t.Fatalf("recorded %d events after re-running the sweep, want 3 (one per step)", len(store.events))
	}
	for i, ev := range store.events {
		if ev.StepIndex != i+1 || ev.RecordID != rec.ID || ev.StateID != state.ID || ev.PolicyID != state.EscalationPolicy.ID {
			t.Errorf("event %d = %+v", i, ev)
		}
		if !ev.BreachedAt.Equal(due[i].FireAt) {
			t.Errorf("event %d breached_at = %v, want the timer's fire_at %v", i, ev.BreachedAt, due[i].FireAt)
		}
		if ev.NotifiedAt != nil {
			t.Errorf("event %d has notified_at set: notification belongs to the action executor", i)
		}
	}
}

func TestEscalationHandlerIgnoresARecordThatHasMovedOn(t *testing.T) {
	eoSLAFreezeClock(t)
	state := eoSLAState(eoSLAInt(3), "days", 0)
	rec := eoSLARecord()
	elsewhere := uuid.New()
	rec.StateID = &elsewhere // the transition's cancel lost the race with an in-flight claim
	due := eoSLAScheduledFor(t, rec, state)

	store := &eoFakeEventStore{}
	if err := NewEOEscalationHandler(store, &eoFakeRecordLookup{rec: rec})(context.Background(), due[0]); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(store.events) != 0 {
		t.Errorf("recorded %d breaches for a record that left the state, want 0", len(store.events))
	}
}

func TestEscalationHandlerReportsAGoneRecord(t *testing.T) {
	eoSLAFreezeClock(t)
	state := eoSLAState(eoSLAInt(3), "days", 0)
	due := eoSLAScheduledFor(t, eoSLARecord(), state)
	err := NewEOEscalationHandler(&eoFakeEventStore{}, &eoFakeRecordLookup{rec: nil})(context.Background(), due[0])
	if !errors.Is(err, ErrEOTimerRecordGone) {
		t.Errorf("handler error = %v, want ErrEOTimerRecordGone so the monitor cancels the record's timers", err)
	}
}

func TestEscalationHandlerSurfacesAStoreFailure(t *testing.T) {
	eoSLAFreezeClock(t)
	state := eoSLAState(eoSLAInt(3), "days", 0)
	rec := eoSLARecord()
	rec.StateID = &state.ID
	due := eoSLAScheduledFor(t, rec, state)
	err := NewEOEscalationHandler(&eoFakeEventStore{err: errors.New("db down")}, &eoFakeRecordLookup{rec: rec})(context.Background(), due[0])
	if err == nil {
		t.Error("a failed event write was swallowed; the timer would be marked fired with no breach recorded")
	}
}

func TestEscalationHandlerRejectsAForeignTimer(t *testing.T) {
	tm := models.EOTimer{CapabilityKey: "reminder", Payload: []byte(`{}`)}
	if err := NewEOEscalationHandler(&eoFakeEventStore{}, &eoFakeRecordLookup{})(context.Background(), tm); err == nil {
		t.Error("handler accepted a timer that is not an escalation timer")
	}
}

// ── wiring into the transition ──────────────────────────────────────────────

type eoFakePolicies struct {
	repository.EscalationPolicyRepository
	policy *models.EscalationPolicy
	err    error
	loads  int
}

func (f *eoFakePolicies) FindByID(context.Context, uuid.UUID) (*models.EscalationPolicy, error) {
	f.loads++
	return f.policy, f.err
}

// eoSLAWiredFixture is the transition fixture with SLA clocks switched on: the
// "review" state has a 3-day SLA and a three-step policy.
func eoSLAWiredFixture(t *testing.T) (*eoTxFixture, *eoSLATimers, *eoFakePolicies) {
	t.Helper()
	eoSLAFreezeClock(t)
	f := eoNewTxFixture(t)
	timers := &eoSLATimers{}
	f.records.txTimers = timers
	sla := eoSLAState(eoSLAInt(3), "days", 0, 96, 264)
	policies := &eoFakePolicies{policy: sla.EscalationPolicy}
	for i := range f.workflows.wf.States {
		st := &f.workflows.wf.States[i]
		if st.ID == f.review.ID || st.ID == f.closed.ID {
			st.SLAHours, st.SLAUnit, st.EscalationPolicyID = sla.SLAHours, sla.SLAUnit, sla.EscalationPolicyID
		}
	}
	f.svc = NewEOTransitionServiceWithSLA(f.records, f.types, f.workflows, f.actions, policies)
	return f, timers, policies
}

func TestTransitionStartsTheEnteredStatesSLAClockOnTheTransactionHandle(t *testing.T) {
	f, timers, _ := eoSLAWiredFixture(t)
	// draft -> review: put the record in draft first.
	f.rec.StateID = &f.draft.ID
	if _, err := f.run("submit", f.actor, models.EOTransitionInput{Values: map[string]any{"he_reason": "r", "priority": "p"}}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(timers.scheduled) != 3 {
		t.Fatalf("scheduled %d timers entering a state with a three-step ladder, want 3", len(timers.scheduled))
	}
	for _, tm := range timers.scheduled {
		if tm.RecordID != f.rec.ID {
			t.Errorf("timer for record %s, want %s", tm.RecordID, f.rec.ID)
		}
	}
	if len(timers.cancelled) != 1 || timers.cancelled[0] != EOEscalationCapabilityKey {
		t.Errorf("cancelled = %v, want the previous state's escalation timers only", timers.cancelled)
	}
}

func TestTransitionOutOfASLAStateCancelsItsClockAndStartsNone(t *testing.T) {
	f, timers, _ := eoSLAWiredFixture(t)
	// review -> escalated: leaves the SLA state for one with no SLA.
	if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if len(timers.scheduled) != 0 {
		t.Errorf("scheduled %d timers entering a state with no SLA, want 0", len(timers.scheduled))
	}
	if len(timers.cancelled) != 1 {
		t.Errorf("cancelled %d times, want 1: leaving the state ends its clock", len(timers.cancelled))
	}
}

func TestTransitionIntoATerminalStateStartsNoClock(t *testing.T) {
	f, timers, policies := eoSLAWiredFixture(t)
	if _, err := f.run("close", f.actor, models.EOTransitionInput{Values: map[string]any{"closure_note": "done"}}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(timers.scheduled) != 0 || policies.loads != 0 {
		t.Errorf("closing scheduled %d timers (policy loads %d), want none: a closed record does not escalate", len(timers.scheduled), policies.loads)
	}
}

func TestTransitionFailureLeavesTheSLAClockAlone(t *testing.T) {
	f, timers, _ := eoSLAWiredFixture(t)
	f.rec.StateID = &f.draft.ID
	// closure_note is required at closed, and the record is in the wrong state
	// for "close" anyway: any refusal must come before a timer is touched.
	if _, err := f.run("close", f.actor, models.EOTransitionInput{}); err == nil {
		t.Fatal("close from draft succeeded")
	}
	if len(timers.scheduled) != 0 || len(timers.cancelled) != 0 {
		t.Errorf("a refused transition touched timers: scheduled %d, cancelled %d", len(timers.scheduled), len(timers.cancelled))
	}
}

func TestTransitionFailsWhenTheSLAClockCannotBeScheduled(t *testing.T) {
	f, timers, _ := eoSLAWiredFixture(t)
	f.rec.StateID = &f.draft.ID
	timers.scheduleErr = errors.New("db down")
	if _, err := f.run("submit", f.actor, models.EOTransitionInput{Values: map[string]any{"he_reason": "r", "priority": "p"}}); err == nil {
		t.Fatal("transition committed without its SLA clock")
	}
	if f.records.committed {
		t.Error("the transaction committed")
	}
}

func TestTransitionWithoutSLAWiringTouchesNoTimers(t *testing.T) {
	f := eoNewTxFixture(t) // txTimers is nil: touching it would panic
	if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
		t.Fatalf("raise: %v", err)
	}
}

func TestTransitionSkipsAMissingPolicyInsteadOfBlockingTheMove(t *testing.T) {
	f, timers, policies := eoSLAWiredFixture(t)
	f.rec.StateID = &f.draft.ID
	policies.policy, policies.err = nil, gorm.ErrRecordNotFound
	if _, err := f.run("submit", f.actor, models.EOTransitionInput{Values: map[string]any{"he_reason": "r", "priority": "p"}}); err != nil {
		t.Fatalf("a deleted policy blocked the transition: %v", err)
	}
	if len(timers.scheduled) != 0 {
		t.Errorf("scheduled %d timers from a policy that does not exist", len(timers.scheduled))
	}
}
