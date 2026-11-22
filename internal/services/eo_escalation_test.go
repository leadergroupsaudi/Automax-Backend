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

// ── creation starts the initial state's clock ──────────────────────────────

// eoSLAStatefulTimers keeps each timer's status, so a create and a later
// transition can share one and the test can ask what is still pending.
type eoSLAStatefulTimers struct {
	repository.EOTimerRepository
	timers []models.EOTimer
}

func (f *eoSLAStatefulTimers) Schedule(_ context.Context, t *models.EOTimer) error {
	c := *t
	c.Status = models.EOTimerPending
	f.timers = append(f.timers, c)
	return nil
}

func (f *eoSLAStatefulTimers) CancelForRecordByCapability(_ context.Context, id uuid.UUID, key string) error {
	for i := range f.timers {
		if f.timers[i].RecordID == id && f.timers[i].CapabilityKey == key && f.timers[i].Status == models.EOTimerPending {
			f.timers[i].Status = models.EOTimerCancelled
		}
	}
	return nil
}

func (f *eoSLAStatefulTimers) pending() []models.EOTimer {
	var out []models.EOTimer
	for _, t := range f.timers {
		if t.Status == models.EOTimerPending {
			out = append(out, t)
		}
	}
	return out
}

// eoSLACreateFixture is the record-service fixture whose initial state carries
// the given SLA and a three-step ladder (delays 0, 96, 264 hours).
func eoSLACreateFixture(t *testing.T, sla *int, unit string) (*eoSvcFixture, *eoSLAStatefulTimers, *eoFakePolicies) {
	t.Helper()
	eoSLAFreezeClock(t)
	f := eoNewSvcFixture(t)
	timers := &eoSLAStatefulTimers{}
	f.records.txTimers = timers
	st := eoSLAState(sla, unit, 0, 96, 264)
	policies := &eoFakePolicies{policy: st.EscalationPolicy}
	s := &f.workflows.workflows[0].States[0]
	s.SLAHours, s.SLAUnit, s.EscalationPolicyID = st.SLAHours, st.SLAUnit, st.EscalationPolicyID
	f.svc = NewEORecordServiceWithSLA(f.records, f.types, f.sequences, f.workflows, policies)
	return f, timers, policies
}

func eoSLACreate(f *eoSvcFixture) (*models.EORecord, error) {
	return f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID: f.recordType.ID,
		Values:       map[string]any{"subject_ar": "توجيه"},
	}, f.actor)
}

func TestCreateStartsTheInitialStatesSLAClock(t *testing.T) {
	cases := []struct {
		unit string
		sla  int
		want time.Duration
	}{
		{"minutes", 3, 3 * time.Minute},
		{"hours", 3, 3 * time.Hour},
		{"days", 3, 72 * time.Hour},
		{"months", 1, 30 * 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.unit, func(t *testing.T) {
			f, timers, _ := eoSLACreateFixture(t, eoSLAInt(tc.sla), tc.unit)
			rec, err := eoSLACreate(f)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if len(timers.timers) != 3 {
				t.Fatalf("scheduled %d timers at creation, want 3", len(timers.timers))
			}
			for i, tm := range timers.timers {
				if tm.RecordID != rec.ID || tm.RecordTypeID != rec.RecordTypeID {
					t.Errorf("timer %d is for %s/%s, want the created record", i, tm.RecordID, tm.RecordTypeID)
				}
				delay := []time.Duration{0, 96 * time.Hour, 264 * time.Hour}[i]
				if got := tm.FireAt.Sub(eoSLAEpoch); got != tc.want+delay {
					t.Errorf("timer %d fires %v after creation, want %v", i, got, tc.want+delay)
				}
			}
		})
	}
}

func TestCreateWithNilOrZeroSLAOrNoneSchedulesNothing(t *testing.T) {
	for name, sla := range map[string]*int{"nil": nil, "zero": eoSLAInt(0)} {
		t.Run(name, func(t *testing.T) {
			f, timers, policies := eoSLACreateFixture(t, sla, "days")
			if _, err := eoSLACreate(f); err != nil {
				t.Fatalf("create: %v", err)
			}
			if len(timers.timers) != 0 || policies.loads != 0 {
				t.Errorf("scheduled %d timers (policy loads %d) for a state with no SLA, want none", len(timers.timers), policies.loads)
			}
		})
	}
	t.Run("no policy", func(t *testing.T) {
		f, timers, _ := eoSLACreateFixture(t, eoSLAInt(3), "days")
		f.workflows.workflows[0].States[0].EscalationPolicyID = nil
		if _, err := eoSLACreate(f); err != nil {
			t.Fatalf("create: %v", err)
		}
		if len(timers.timers) != 0 {
			t.Errorf("scheduled %d timers with no policy, want 0", len(timers.timers))
		}
	})
}

func TestCreateWithoutSLAWiringTouchesNoTimers(t *testing.T) {
	f := eoNewSvcFixture(t) // txTimers is nil: touching it would panic
	if _, err := eoSLACreate(f); err != nil {
		t.Fatalf("create: %v", err)
	}
}

func TestCreateLeavesNoTimerWhenTheCreateFails(t *testing.T) {
	f, timers, _ := eoSLACreateFixture(t, eoSLAInt(3), "days")
	f.records.historyErr = errors.New("db down")
	if _, err := eoSLACreate(f); err == nil {
		t.Fatal("create succeeded despite a failed history write")
	}
	if len(timers.timers) != 0 {
		t.Errorf("a failed create scheduled %d timers", len(timers.timers))
	}
}

func TestCreateFailsWhenTheSLAClockCannotBeScheduled(t *testing.T) {
	f, _, _ := eoSLACreateFixture(t, eoSLAInt(3), "days")
	f.records.txTimers = &eoSLATimers{scheduleErr: errors.New("db down")}
	if _, err := eoSLACreate(f); err == nil {
		t.Fatal("a record was created without its SLA clock")
	}
}

// The first timers belong to the initial state; leaving it must cancel them.
func TestTransitionOutOfTheInitialStateCancelsTheClockCreationStarted(t *testing.T) {
	cf, timers, _ := eoSLACreateFixture(t, eoSLAInt(3), "days")
	created, err := eoSLACreate(cf)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := len(timers.pending()); got != 3 {
		t.Fatalf("pending after create = %d, want 3", got)
	}

	// Move the same record out of draft, through the transition service, onto
	// the same timer store.
	tf := eoNewTxFixture(t)
	tf.records.txTimers = timers
	tf.svc = NewEOTransitionServiceWithSLA(tf.records, tf.types, tf.workflows, tf.actions, &eoFakePolicies{})
	tf.rec.ID, tf.rec.StateID = created.ID, &tf.draft.ID
	tf.records.stored[tf.rec.ID] = tf.rec
	if _, err := tf.run("submit", tf.actor, models.EOTransitionInput{Values: map[string]any{"he_reason": "r", "priority": "p"}}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := len(timers.pending()); got != 0 {
		t.Errorf("%d creation timers still pending after the record left its initial state, want 0", got)
	}
	for _, tm := range timers.timers {
		if tm.Status != models.EOTimerCancelled {
			t.Errorf("timer status = %q, want cancelled", tm.Status)
		}
	}
}
