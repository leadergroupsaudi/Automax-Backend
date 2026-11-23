package handlers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/utils"
)

// EOSignoffHandler serves the two obligations the Executive Office pilot owes
// its client, and the one operation that discharges them.
//
// WHAT THE OBLIGATIONS ARE. The pilot's Arabic field labels were written by an
// implementer, not supplied by the client, and its workflow states and
// transitions were DERIVED from the client's field matrix rather than
// transcribed from the Core Function Flows, which are not in this repository.
// Both facts are recorded in the data — eo_field_defs.ar_untranslated,
// workflow_states.eo_client_unconfirmed and
// workflow_transitions.eo_client_unconfirmed — so that the gap is a query
// rather than a memory.
//
// WHY THERE IS A ROUTE AT ALL. Two gaps composed into one problem. The markers
// were WRITE-ONLY: nothing in the application could clear one, so recording a
// client sign-off meant hand-written SQL against a government deployment. And
// the only thing that read them was the boot log, which (until Plan 2's fix
// wave) was reachable only on the boot that applied the pilot seed. So there
// was no supported way to record a sign-off and no live way to ask what was
// still outstanding.
//
// WRITE-ONLY ON THE DESIGNER PATH IS CORRECT AND STAYS. A designer save must
// not clear a sign-off as a side effect of editing a help text, which is why
// eoFieldDefRequest does not carry ar_untranslated and
// handlers.TestFieldDefRequestCannotCarryTheUntranslatedMarker pins its
// absence. This handler is a different thing: a deliberate operation that
// names the rows it clears and records who cleared them.
//
// WHAT AN OPERATOR CAN DO WITH IT, STATED EXACTLY. GET answers what is
// outstanding NOW, with the id of every row. POST clears the marker on the
// rows whose ids it is given, and nothing else — there is deliberately no
// "clear all", because the marker means a human looked at that row. Clearing a
// marker changes NOTHING else: it does not write Arabic, it does not alter a
// state machine, and it does not make a field, a state or a transition
// behave differently. It records that the client has signed the item off.
//
// IT READS THE SAME QUERY THE BOOT REPORT DOES. database.EOUntranslatedLabels
// and database.EOUnconfirmedStateMachines back both this route and
// database.eoReportPilotObligations, so an operator comparing a boot log line
// with this route's counts sees one number, not two.
type EOSignoffHandler struct {
	db *gorm.DB
}

func NewEOSignoffHandler(db *gorm.DB) *EOSignoffHandler {
	return &EOSignoffHandler{db: db}
}

// eoSignoffManage is the permission both routes carry: the type designer's own
// code. Reading what the client still owes and recording that they have signed
// it off are both configuration-administration, not record work, and nothing
// below is scoped per record type — so eo:view would be too wide and a code of
// its own would be one more thing to grant on a deployment that already grants
// this one.
const eoSignoffManage = "eo:types:manage"

// RegisterRoutes registers the sign-off routes on r.
//
// Refusal is 403 and never 401: the frontend's axios interceptor logs the user
// out on a 401, so a missing permission must not look like a missing session.
// The caller's `perm` is authMiddleware.RequirePermission, which answers 403
// for a caller who holds none of the codes and short-circuits for a super
// admin.
func (h *EOSignoffHandler) RegisterRoutes(r fiber.Router, perm EOPermissionMiddleware) {
	r.Get("/", perm(eoSignoffManage), h.List)
	r.Post("/discharge", perm(eoSignoffManage), h.Discharge)
}

// ── GET: what is outstanding now ────────────────────────────────────────────

type eoSignoffLabel struct {
	ID       uuid.UUID `json:"id"`
	TypeKey  string    `json:"type_key"`
	FieldKey string    `json:"field_key"`
	Label    string    `json:"label"`
}

type eoSignoffStep struct {
	ID           uuid.UUID `json:"id"`
	WorkflowCode string    `json:"workflow_code"`
	Code         string    `json:"code"`
	Name         string    `json:"name"`
}

type eoSignoffCounts struct {
	UntranslatedLabels     int `json:"untranslated_labels"`
	UnconfirmedStates      int `json:"unconfirmed_states"`
	UnconfirmedTransitions int `json:"unconfirmed_transitions"`
}

type eoSignoffList struct {
	UntranslatedLabels     []eoSignoffLabel `json:"untranslated_labels"`
	UnconfirmedStates      []eoSignoffStep  `json:"unconfirmed_states"`
	UnconfirmedTransitions []eoSignoffStep  `json:"unconfirmed_transitions"`
	Counts                 eoSignoffCounts  `json:"counts"`
}

// eoSignoffOutstanding reads both obligations from db. It is a LIVE read on
// every call — a list computed once and cached would be the defect this
// programme has already shipped in the seed, where a gate that could never
// reopen made a report describe the database as it had been rather than as it
// is.
func eoSignoffOutstanding(db *gorm.DB) (*eoSignoffList, error) {
	labels, err := database.EOUntranslatedLabels(db)
	if err != nil {
		return nil, err
	}
	steps, err := database.EOUnconfirmedStateMachines(db)
	if err != nil {
		return nil, err
	}

	out := &eoSignoffList{
		UntranslatedLabels:     make([]eoSignoffLabel, 0, len(labels)),
		UnconfirmedStates:      make([]eoSignoffStep, 0),
		UnconfirmedTransitions: make([]eoSignoffStep, 0),
	}
	for _, l := range labels {
		out.UntranslatedLabels = append(out.UntranslatedLabels, eoSignoffLabel{
			ID: l.ID, TypeKey: l.TypeKey, FieldKey: l.FieldKey, Label: l.Label,
		})
	}
	for _, s := range steps {
		entry := eoSignoffStep{ID: s.ID, WorkflowCode: s.WorkflowCode, Code: s.Code, Name: s.Name}
		switch s.Kind {
		case eoSignoffKindState:
			out.UnconfirmedStates = append(out.UnconfirmedStates, entry)
		case eoSignoffKindTransition:
			out.UnconfirmedTransitions = append(out.UnconfirmedTransitions, entry)
		default:
			// The query writes one of the two literals and nothing else. A
			// third would mean a row of unknown provenance, and dropping it
			// silently would understate what the client still owes.
			return nil, fmt.Errorf("sign-off query returned kind %q for %s", s.Kind, s.ID)
		}
	}
	out.Counts = eoSignoffCounts{
		UntranslatedLabels:     len(out.UntranslatedLabels),
		UnconfirmedStates:      len(out.UnconfirmedStates),
		UnconfirmedTransitions: len(out.UnconfirmedTransitions),
	}
	return out, nil
}

// eoSignoffKindState and eoSignoffKindTransition are the two values
// database.EOUnconfirmedStep.Kind carries. They say which TABLE an id is in,
// which is why the list returns states and transitions as two arrays rather
// than one: the ids alone do not say. The discharge keeps them apart the
// other way round, by looking each id up in the table its request field
// names, so a transition id sent in state_ids is refused rather than
// silently ignored.
const (
	eoSignoffKindState      = "state"
	eoSignoffKindTransition = "transition"
)

// List answers what the client still owes, as the database stands now.
func (h *EOSignoffHandler) List(c *fiber.Ctx) error {
	out, err := eoSignoffOutstanding(h.db)
	if err != nil {
		return utils.InternalErrorResponse(c, err, "Failed to read the EO sign-off obligations")
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "EO sign-off obligations", out)
}

// ── POST: discharge the rows the client has signed off ──────────────────────

// eoSignoffDischargeRequest names the rows to discharge. The three lists are
// separate because the id alone does not say which table it is in, and a
// single mixed list would let a mistyped id clear the wrong kind of row.
//
// THERE IS NO "ALL" FLAG AND THERE MUST NOT BE ONE. The marker exists to say
// that a human looked at that row; an operation that cleared every marker at
// once would be exactly the hand-written UPDATE this route replaced, with an
// audit entry attached.
type eoSignoffDischargeRequest struct {
	FieldDefIDs   []uuid.UUID `json:"field_def_ids"`
	StateIDs      []uuid.UUID `json:"state_ids"`
	TransitionIDs []uuid.UUID `json:"transition_ids"`
}

type eoSignoffDischargeCounts struct {
	FieldDefs   int `json:"field_defs"`
	States      int `json:"states"`
	Transitions int `json:"transitions"`
}

type eoSignoffDischargeResult struct {
	Discharged eoSignoffDischargeCounts `json:"discharged"`
	// AlreadyClear are ids that exist and carried no marker. They are
	// reported rather than refused so that retrying a batch that half
	// succeeded is not an error.
	AlreadyClear []string `json:"already_clear"`
	// Remaining is what is still outstanding after this discharge, read back
	// from the database, so the caller does not have to ask again to know
	// whether the number moved.
	Remaining eoSignoffCounts `json:"remaining"`
}

// Discharge clears the markers on the rows it is given, and only those.
//
// IT IS ALL OR NOTHING. An id the database does not know is a mistake in the
// list the operator is working from, and applying the rest of the batch would
// discharge rows while the operator believed the whole request had failed. The
// updates and the audit entry are written in ONE transaction, so a discharge
// that was not recorded did not happen, and a recorded one did.
func (h *EOSignoffHandler) Discharge(c *fiber.Ctx) error {
	var req eoSignoffDischargeRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body: "+err.Error())
	}
	fieldIDs := eoSignoffDistinct(req.FieldDefIDs)
	stateIDs := eoSignoffDistinct(req.StateIDs)
	transitionIDs := eoSignoffDistinct(req.TransitionIDs)
	if len(fieldIDs)+len(stateIDs)+len(transitionIDs) == 0 {
		return utils.ErrorResponse(c, fiber.StatusBadRequest,
			"Name the rows to discharge in field_def_ids, state_ids or transition_ids. There is no clear-all: each marker records that a person reviewed that row.")
	}

	// WHO. The discharge asserts that a human reviewed something, so it is
	// recorded against a person or it does not happen. The routes run behind
	// authMiddleware.RequirePermission, which loads the user and puts it in
	// Locals (Authenticate before it puts only the user ID there), so an
	// absent one is a wiring fault — refused with 403 and never
	// 401, which the frontend treats as a logout.
	actor, _ := c.Locals(constants.ContextKeys.User).(*models.User)
	if actor == nil || actor.ID == uuid.Nil {
		return utils.ErrorResponse(c, fiber.StatusForbidden,
			"The acting user could not be identified, and a sign-off is recorded against a person")
	}

	var result eoSignoffDischargeResult
	var badRequest error
	err := h.db.Transaction(func(tx *gorm.DB) error {
		// THE SAME QUERY THE LIST USES decides what is outstanding, so what
		// can be discharged is exactly what GET showed. A second predicate
		// written here could disagree with it — and then an operator would be
		// refused a row the list had just offered, or clear one it had
		// hidden.
		outstanding, err := eoSignoffOutstanding(tx)
		if err != nil {
			return err
		}
		open := map[uuid.UUID]bool{}
		for _, l := range outstanding.UntranslatedLabels {
			open[l.ID] = true
		}
		for _, s := range append(append([]eoSignoffStep{}, outstanding.UnconfirmedStates...), outstanding.UnconfirmedTransitions...) {
			open[s.ID] = true
		}

		var alreadyClear []string
		plan := []struct {
			ids    []uuid.UUID
			model  any
			marker string
			what   string
			count  *int
		}{
			{fieldIDs, &models.EOFieldDef{}, "ar_untranslated", "field definition", &result.Discharged.FieldDefs},
			{stateIDs, &models.WorkflowState{}, "eo_client_unconfirmed", "workflow state", &result.Discharged.States},
			{transitionIDs, &models.WorkflowTransition{}, "eo_client_unconfirmed", "workflow transition", &result.Discharged.Transitions},
		}
		for _, p := range plan {
			clear, clearAlready, err := eoSignoffPartition(tx, p.model, p.ids, open, p.what)
			if err != nil {
				badRequest = err
				return err
			}
			alreadyClear = append(alreadyClear, clearAlready...)
			if len(clear) == 0 {
				continue
			}
			res := tx.Model(p.model).Where("id IN ?", clear).Update(p.marker, false)
			if res.Error != nil {
				return fmt.Errorf("discharge %s markers: %w", p.what, res.Error)
			}
			// The rows were outstanding a moment ago in this same
			// transaction, so a short count means something else changed them
			// and this request no longer describes what it is doing.
			if int(res.RowsAffected) != len(clear) {
				return fmt.Errorf("discharge %s markers: %d of %d rows changed", p.what, res.RowsAffected, len(clear))
			}
			*p.count = len(clear)
		}
		sort.Strings(alreadyClear)
		result.AlreadyClear = alreadyClear

		return eoSignoffAudit(tx, c, actor, fieldIDs, stateIDs, transitionIDs, result.Discharged, alreadyClear)
	})
	if err != nil {
		if badRequest != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, badRequest.Error())
		}
		return utils.InternalErrorResponse(c, err, "Failed to discharge the EO sign-off markers")
	}

	after, err := eoSignoffOutstanding(h.db)
	if err != nil {
		return utils.InternalErrorResponse(c, err, "The markers were discharged but the remaining obligations could not be read")
	}
	result.Remaining = after.Counts
	if result.AlreadyClear == nil {
		result.AlreadyClear = []string{}
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "EO sign-off markers discharged", result)
}

// eoSignoffPartition splits ids into the ones that are outstanding and the
// ones that exist but carry no marker, and fails on an id the table does not
// hold at all.
//
// The existence probe is what separates "already signed off" — which is a
// retry and is fine — from "this id is not a row of this kind", which is the
// operator working from the wrong list or pasting a state id into
// transition_ids. The second must refuse the WHOLE request: a partial apply
// is the failure mode that leaves a government deployment in a state nobody
// asked for.
func eoSignoffPartition(tx *gorm.DB, model any, ids []uuid.UUID, open map[uuid.UUID]bool, what string) (clear []uuid.UUID, alreadyClear []string, err error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	var present []uuid.UUID
	if err := tx.Model(model).Where("id IN ?", ids).Pluck("id", &present).Error; err != nil {
		return nil, nil, fmt.Errorf("read the %ss named: %w", what, err)
	}
	exists := make(map[uuid.UUID]bool, len(present))
	for _, id := range present {
		exists[id] = true
	}
	var unknown []string
	for _, id := range ids {
		switch {
		case open[id] && exists[id]:
			clear = append(clear, id)
		case exists[id]:
			alreadyClear = append(alreadyClear, id.String())
		default:
			unknown = append(unknown, id.String())
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, nil, fmt.Errorf("no %s has id %s, so nothing was discharged", what, strings.Join(unknown, ", "))
	}
	return clear, alreadyClear, nil
}

// eoSignoffDistinct drops duplicates and the zero uuid, preserving order. A
// repeated id would otherwise be counted twice in the audit entry.
func eoSignoffDistinct(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// ── the audit entry ─────────────────────────────────────────────────────────

// eoSignoffAuditModule and eoSignoffAuditAction are how the entry is found
// again. The action log's filter options are SELECT DISTINCT over the stored
// rows (repository.actionLogRepository.GetDistinctModules/Actions), so a pair
// of values of its own appears in the existing action-log screen by itself
// and needs no registration anywhere.
const (
	eoSignoffAuditModule = "eo_signoff"
	eoSignoffAuditAction = "discharge"
)

// eoSignoffAuditEntry is what the entry's new_value holds: the ids this
// request discharged, by kind, and the ids it found already clear. It is a
// struct rather than a sentence because the ids are the point — "an
// administrator discharged 3 markers" is not an audit of a sign-off.
type eoSignoffAuditEntry struct {
	FieldDefIDs   []uuid.UUID `json:"field_def_ids,omitempty"`
	StateIDs      []uuid.UUID `json:"state_ids,omitempty"`
	TransitionIDs []uuid.UUID `json:"transition_ids,omitempty"`
	AlreadyClear  []string    `json:"already_clear,omitempty"`
}

// eoSignoffAudit writes the audit entry IN THE DISCHARGE'S OWN TRANSACTION.
//
// WHY NOT services.ActionLogService, WHICH IS WHAT EVERYTHING ELSE USES. That
// service writes through its own handle, so its entry cannot join this
// transaction: it would commit for a discharge that rolled back, or be lost
// for one that committed. For an ordinary admin edit that is an acceptable
// looseness — the middleware's entry is a record that the call happened. Here
// the entry is the only durable statement of WHO asserted that the client
// signed these rows off, so it is written with the rows or not at all. The
// row it writes is exactly the shape ActionLogService writes, so it appears in
// the existing action-log screen like any other.
//
// WHAT middleware.ActionLogger ADDS, AND WHY IT IS STILL ON THE GROUP (see
// cmd/server/main.go). It logs the CALL — including one that never reaches
// this function, such as a malformed body or a refusal — but it captures no
// request body, so it can say that somebody posted to this route and not which
// rows they named. The two entries are complementary and neither replaces the
// other.
func eoSignoffAudit(tx *gorm.DB, c *fiber.Ctx, actor *models.User, fieldIDs, stateIDs, transitionIDs []uuid.UUID, counts eoSignoffDischargeCounts, alreadyClear []string) error {
	detail, err := json.Marshal(eoSignoffAuditEntry{
		FieldDefIDs:   fieldIDs,
		StateIDs:      stateIDs,
		TransitionIDs: transitionIDs,
		AlreadyClear:  alreadyClear,
	})
	if err != nil {
		return fmt.Errorf("render the sign-off audit entry: %w", err)
	}
	ip, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	agent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)

	entry := models.ActionLog{
		UserID: actor.ID,
		Action: eoSignoffAuditAction,
		Module: eoSignoffAuditModule,
		Description: fmt.Sprintf("Discharged EO client sign-off markers: %d field label(s), %d workflow state(s), %d transition(s)",
			counts.FieldDefs, counts.States, counts.Transitions),
		NewValue:  string(detail),
		IPAddress: ip,
		UserAgent: agent,
		Status:    "success",
		CreatedAt: time.Now(),
	}
	// Omit("User"): the association is nil here, and gorm must not try to
	// upsert the caller's user row as a side effect of auditing.
	if err := tx.Omit("User").Create(&entry).Error; err != nil {
		return fmt.Errorf("record the sign-off in the action log: %w", err)
	}
	return nil
}
