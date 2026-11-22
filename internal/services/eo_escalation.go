package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
)

// EOEscalationCapabilityKey is the capability_key of an SLA escalation timer.
const EOEscalationCapabilityKey = "eo.escalation"

// eoSLANow is the clock an SLA starts on. A test seam.
var eoSLANow = time.Now

// eoEscalationPayload is what an escalation timer carries to its handler.
// StepIndex is the policy step's StepOrder.
type eoEscalationPayload struct {
	StateID   uuid.UUID `json:"state_id"`
	PolicyID  uuid.UUID `json:"policy_id"`
	StepIndex int       `json:"step_index"`
}

// ScheduleStateSLA starts the state's SLA clock for a record that has just
// entered it: one durable timer per step of the state's escalation policy.
//
// The configuration is the incident product's — the state's SLA fields and its
// EscalationPolicy — read the way escalation.go reads it, so a step fires
// DelayHours after the SLA deadline, and DelayHours = 0 fires at the deadline.
// state.EscalationPolicy must be loaded with its steps.
//
// The deadline is state.SLADuration(), never SLAHours: SLAHours is a number in
// SLAUnit (minutes, hours, days, months), so 3 with "days" is 72 hours. A nil
// SLAHours and a zero one both mean "no SLA" and schedule nothing; a zero
// deadline would fire at once. DelayHours is hours and has no unit field, so a
// step cannot be placed less than an hour apart from another.
//
// Inside a transition, timers must be the transaction-bound handle
// (EORecordRepository.Transaction hands one out), so that a transition that
// rolls back leaves no timer behind.
func ScheduleStateSLA(ctx context.Context, rec *models.EORecord, state *models.WorkflowState, timers repository.EOTimerRepository) error {
	if rec == nil || state == nil || timers == nil {
		return errors.New("schedule state sla: record, state and timer repository are required")
	}
	sla := state.SLADuration()
	if sla <= 0 || state.EscalationPolicyID == nil {
		return nil
	}
	policy := state.EscalationPolicy
	if policy == nil {
		return fmt.Errorf("schedule state sla: state %q references escalation policy %s but it was not loaded", state.Name, *state.EscalationPolicyID)
	}
	if !policy.IsActive || len(policy.Steps) == 0 {
		return nil
	}

	steps := append([]models.EscalationPolicyStep(nil), policy.Steps...)
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].StepOrder < steps[j].StepOrder })
	for i := 1; i < len(steps); i++ {
		if steps[i].StepOrder == steps[i-1].StepOrder {
			return fmt.Errorf("schedule state sla: policy %q has two steps with step_order %d", policy.Name, steps[i].StepOrder)
		}
	}

	entered := eoSLANow()
	for _, step := range steps {
		payload, err := json.Marshal(eoEscalationPayload{StateID: state.ID, PolicyID: policy.ID, StepIndex: step.StepOrder})
		if err != nil {
			return fmt.Errorf("schedule state sla: encode payload: %w", err)
		}
		if err := timers.Schedule(ctx, &models.EOTimer{
			RecordID:      rec.ID,
			RecordTypeID:  rec.RecordTypeID,
			FireAt:        entered.Add(sla + time.Duration(step.DelayHours)*time.Hour),
			CapabilityKey: EOEscalationCapabilityKey,
			Payload:       payload,
		}); err != nil {
			return fmt.Errorf("schedule state sla step %d: %w", step.StepOrder, err)
		}
	}
	return nil
}

// EOCancelStateSLA stops the record's SLA clock, when it leaves the state the
// clock belongs to. It cancels escalation timers only: the record's other
// timers (a reminder) are not the SLA's to cancel. Like ScheduleStateSLA it
// belongs on the transaction-bound handle.
func EOCancelStateSLA(ctx context.Context, rec *models.EORecord, timers repository.EOTimerRepository) error {
	if rec == nil || timers == nil {
		return errors.New("cancel state sla: record and timer repository are required")
	}
	return timers.CancelForRecordByCapability(ctx, rec.ID, EOEscalationCapabilityKey)
}

// NewEOEscalationHandler is the EOTimerHandler for escalation timers. A fired
// step is a breach, and the handler's whole job is to record it: one
// eo_escalation_events row per step, idempotent under the monitor's
// at-least-once delivery. It does not notify. Sending is the action
// executor's, and NotifiedAt stays nil for whatever delivers the message to
// set.
//
// Timers are cancelled when the record leaves the state, but a timer already
// claimed by a worker can still arrive, so the handler checks that the record
// is still in the state the timer was scheduled for and does nothing if not.
func NewEOEscalationHandler(events repository.EOEscalationEventRepository, records repository.EORecordRepository) EOTimerHandler {
	return func(ctx context.Context, timer models.EOTimer) error {
		if timer.CapabilityKey != EOEscalationCapabilityKey {
			return fmt.Errorf("escalation handler: timer %s is a %q timer", timer.ID, timer.CapabilityKey)
		}
		var p eoEscalationPayload
		if err := json.Unmarshal(timer.Payload, &p); err != nil {
			return fmt.Errorf("escalation handler: timer %s payload: %w", timer.ID, err)
		}
		rec, err := records.GetAnyByID(ctx, timer.RecordID)
		if err != nil {
			return fmt.Errorf("escalation handler: load record %s: %w", timer.RecordID, err)
		}
		if rec == nil {
			return ErrEOTimerRecordGone
		}
		if rec.StateID == nil || *rec.StateID != p.StateID {
			return nil
		}
		_, err = events.Record(ctx, &models.EOEscalationEvent{
			RecordID:     timer.RecordID,
			RecordTypeID: timer.RecordTypeID,
			StateID:      p.StateID,
			PolicyID:     p.PolicyID,
			StepIndex:    p.StepIndex,
			BreachedAt:   timer.FireAt,
		})
		return err
	}
}
