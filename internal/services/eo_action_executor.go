package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// This is the EO action executor. It is a SEPARATE implementation from
// action_executor.go, not an extension of it (plan Deviation 1): that file
// reads about 40 *models.Incident fields, its field_update is hard-wired to
// assignee_id and department_id, its final-close branch writes
// sms_feedback_pending keyed by incident id, and its report-link guard reads
// and writes incident-only columns — and it has no test file. None of that is
// carried over: there is no sms_feedback_pending call and no report-link
// once-per-closure guard, because an EO record has neither the table row nor
// the columns.
//
// The contract that IS preserved is that ExecuteActions always returns nil: a
// failing action never fails the transition (action_executor.go:81). The EO
// transition path dispatches after its commit, fire and forget, and a state
// change that is already durable must not be undone by a notification that
// did not go out. Per-action errors are logged.
//
// Placeholders. The incident side has two separate, non-overlapping mechanisms
// — replacePlaceholders ({{key}}) for inline text and BuildIncidentVariables
// (bare keys) for template codes. Here both are served from ONE map built
// once per dispatch (eoActionVars), so an inline template and a template code
// can never disagree about what a key means or which keys exist.

// EOActionNotifier is the part of *NotificationService this executor calls.
type EOActionNotifier interface {
	SendNotification(ctx context.Context, channel string, templateCode *string, language string, to []string, cc []string, bcc []string, subject string, body string, variables map[string]string, attachments []models.AttachmentData, sentBy *uuid.UUID, sessionID *uuid.UUID) (*SendNotificationResult, error)
}

// EOActionExecutor dispatches the actions configured on a transition. It is
// the EOTransitionActionDispatcher task 14's service calls after its commit.
type EOActionExecutor interface {
	ExecuteActions(ctx context.Context, rec *models.EORecord, transition *models.WorkflowTransition, performedBy *models.User) error
}

type eoActionExecutor struct {
	recordSvc  EORecordService
	records    repository.EORecordRepository
	types      repository.EORecordTypeRepository
	users      repository.UserRepository
	depts      repository.DepartmentRepository
	notifier   EOActionNotifier
	httpClient *http.Client

	// async tracks IsAsync actions so a test can wait for them.
	async sync.WaitGroup
}

// NewEOActionExecutor builds the executor. No database handle is held: every
// read and write goes through a service or repository, and the executor runs
// after the transition's commit, so it never holds a transaction.
func NewEOActionExecutor(
	recordSvc EORecordService,
	records repository.EORecordRepository,
	types repository.EORecordTypeRepository,
	users repository.UserRepository,
	depts repository.DepartmentRepository,
	notifier EOActionNotifier,
) EOActionExecutor {
	return &eoActionExecutor{
		recordSvc: recordSvc, records: records, types: types,
		users: users, depts: depts, notifier: notifier,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (e *eoActionExecutor) wait() { e.async.Wait() }

// ExecuteActions runs the transition's active actions in ExecutionOrder.
func (e *eoActionExecutor) ExecuteActions(ctx context.Context, rec *models.EORecord, transition *models.WorkflowTransition, performedBy *models.User) error {
	if rec == nil || transition == nil || len(transition.Actions) == 0 {
		return nil
	}

	// A copy, sorted stably: ties keep their configured order and the
	// caller's slice is not reordered.
	actions := make([]models.TransitionAction, len(transition.Actions))
	copy(actions, transition.Actions)
	sort.SliceStable(actions, func(i, j int) bool { return actions[i].ExecutionOrder < actions[j].ExecutionOrder })

	for i := range actions {
		action := actions[i]
		if !action.IsActive {
			continue
		}
		if action.IsAsync {
			e.async.Add(1)
			go func() {
				defer e.async.Done()
				// Detached from the request: the caller may be gone by
				// the time a background action runs.
				e.runAction(context.WithoutCancel(ctx), &action, rec, transition, performedBy)
			}()
			continue
		}
		e.runAction(ctx, &action, rec, transition, performedBy)
	}
	return nil
}

// runAction runs one action and absorbs its failure — an error or a panic —
// into the log.
func (e *eoActionExecutor) runAction(ctx context.Context, action *models.TransitionAction, rec *models.EORecord, transition *models.WorkflowTransition, performedBy *models.User) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("eo: record %s: action %q (%s) of transition %q panicked: %v", rec.Reference, action.Name, action.ActionType, transition.Code, p)
		}
	}()
	if err := e.dispatch(ctx, action, rec, transition, performedBy); err != nil {
		log.Printf("eo: record %s: action %q (%s) of transition %q failed: %v", rec.Reference, action.Name, action.ActionType, transition.Code, err)
	}
}

func (e *eoActionExecutor) dispatch(ctx context.Context, action *models.TransitionAction, rec *models.EORecord, transition *models.WorkflowTransition, performedBy *models.User) error {
	switch action.ActionType {
	case "notification":
		return e.executeNotification(ctx, action, rec, transition, performedBy)
	case "email":
		return e.executeEmail(ctx, action, rec, transition, performedBy)
	case "sms":
		return e.executeSMS(ctx, action, rec, transition, performedBy)
	case "webhook":
		return e.executeWebhook(ctx, action, rec, transition, performedBy)
	case "field_update":
		return e.executeFieldUpdate(ctx, action, rec, performedBy)
	case "spawn_record":
		return e.executeSpawnRecord(ctx, action, rec, transition, performedBy)
	default:
		return fmt.Errorf("unknown action type: %s", action.ActionType)
	}
}

// ── recipients ──────────────────────────────────────────────────────────────

type eoResolved struct {
	Emails []string
	Phones []string
}

// resolveRecipients turns recipient tokens into addresses. The tokens are
// assignee, creator, owning_body_manager, user:<uuid> and email:<address>.
// A token it does not recognise, or one that names nobody (no assignee, no
// manager, a user that does not exist), resolves to nothing and is logged —
// it never errors, so one stale token does not stop the others being sent.
// Results are de-duplicated, case-insensitively, keeping first-seen order.
func (e *eoActionExecutor) resolveRecipients(ctx context.Context, tokens []string, rec *models.EORecord) eoResolved {
	var out eoResolved
	seenEmail, seenPhone := map[string]bool{}, map[string]bool{}
	addUser := func(id *uuid.UUID, token string) {
		if id == nil || *id == uuid.Nil {
			log.Printf("eo: record %s: recipient %q names nobody", rec.Reference, token)
			return
		}
		u, err := e.users.FindByID(ctx, *id)
		if err != nil || u == nil {
			log.Printf("eo: record %s: recipient %q: user %s not found: %v", rec.Reference, token, *id, err)
			return
		}
		if k := strings.ToLower(strings.TrimSpace(u.Email)); k != "" && !seenEmail[k] {
			seenEmail[k] = true
			out.Emails = append(out.Emails, strings.TrimSpace(u.Email))
		}
		if p := strings.TrimSpace(u.Phone); p != "" && !seenPhone[p] {
			seenPhone[p] = true
			out.Phones = append(out.Phones, p)
		}
	}

	for _, token := range tokens {
		token = strings.TrimSpace(token)
		switch {
		case token == "assignee":
			addUser(rec.AssignedUserID, token)
		case token == "creator":
			id := rec.CreatedByID
			addUser(&id, token)
		case token == "owning_body_manager":
			if rec.OwningBodyID == nil {
				log.Printf("eo: record %s: recipient %q: record has no owning body", rec.Reference, token)
				continue
			}
			d, err := e.depts.FindByID(ctx, *rec.OwningBodyID)
			if err != nil || d == nil {
				log.Printf("eo: record %s: recipient %q: owning body %s not found: %v", rec.Reference, token, *rec.OwningBodyID, err)
				continue
			}
			addUser(d.ManagerID, token)
		case strings.HasPrefix(token, "user:"):
			id, err := uuid.Parse(strings.TrimPrefix(token, "user:"))
			if err != nil {
				log.Printf("eo: record %s: recipient %q: not a uuid", rec.Reference, token)
				continue
			}
			addUser(&id, token)
		case strings.HasPrefix(token, "email:"):
			addr := strings.TrimSpace(strings.TrimPrefix(token, "email:"))
			if k := strings.ToLower(addr); addr != "" && !seenEmail[k] {
				seenEmail[k] = true
				out.Emails = append(out.Emails, addr)
			}
		default:
			log.Printf("eo: record %s: unknown recipient token %q ignored", rec.Reference, token)
		}
	}
	return out
}

// ── placeholders ────────────────────────────────────────────────────────────

var eoPlaceholderRE = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

// eoPlain turns a stored value into a plain JSON-friendly one: pointers are
// dereferenced, uuids become strings and times RFC 3339. An unset pointer is
// nil.
func eoPlain(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case *uuid.UUID:
		if x == nil {
			return nil
		}
		return x.String()
	case uuid.UUID:
		return x.String()
	case *time.Time:
		if x == nil {
			return nil
		}
		return x.UTC().Format(time.RFC3339)
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	default:
		return v
	}
}

func eoString(v any) string {
	switch x := eoPlain(v).(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	default:
		return fmt.Sprint(x)
	}
}

// eoActionVars is the single variable map both placeholder mechanisms render
// from: every field of the record (columns and values bag, by field key) plus
// the record's identity, the transition and the performing user.
func eoActionVars(rec *models.EORecord, transition *models.WorkflowTransition, performedBy *models.User) map[string]string {
	vars := map[string]string{}
	for k, v := range eoFlattenStored(rec) {
		vars[k] = eoString(v)
	}
	vars["record_id"] = rec.ID.String()
	vars["record_type_id"] = rec.RecordTypeID.String()
	vars["reference"] = rec.Reference
	if transition != nil {
		vars["transition_code"] = transition.Code
		vars["transition_name"] = transition.Name
		if transition.FromState != nil {
			vars["from_state"] = transition.FromState.Name
		}
		if transition.ToState != nil {
			vars["to_state"] = transition.ToState.Name
		}
	}
	if performedBy != nil {
		vars["performed_by_name"] = strings.TrimSpace(strings.Join(strings.Fields(performedBy.FirstName+" "+performedBy.MiddleName+" "+performedBy.LastName), " "))
		vars["performed_by_email"] = performedBy.Email
	}
	return vars
}

// eoRender substitutes {{key}} from vars. A key vars does not hold is left as
// written, so a typo is visible in the output rather than silently blank.
// escape, when set, is applied to each substituted value.
func eoRender(tpl string, vars map[string]string, escape func(string) string) string {
	return eoPlaceholderRE.ReplaceAllStringFunc(tpl, func(m string) string {
		key := eoPlaceholderRE.FindStringSubmatch(m)[1]
		v, ok := vars[key]
		if !ok {
			return m
		}
		if escape != nil {
			return escape(v)
		}
		return v
	})
}

func eoJSONEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// ── notification / email / sms / webhook ────────────────────────────────────

type eoNotificationConfig struct {
	Recipients []string `json:"recipients"`
	Title      string   `json:"title"`
	Message    string   `json:"message"`
}

type eoEmailConfig struct {
	Recipients      []string `json:"recipients"`
	TemplateCode    string   `json:"template_code"`
	SubjectTemplate string   `json:"subject_template"`
	BodyTemplate    string   `json:"body_template"`
	Language        string   `json:"language"`
}

type eoSMSConfig struct {
	Recipients      []string `json:"recipients"`
	TemplateCode    string   `json:"template_code"`
	MessageTemplate string   `json:"message_template"`
	Language        string   `json:"language"`
}

type eoWebhookConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

func eoSentBy(u *models.User) *uuid.UUID {
	if u == nil {
		return nil
	}
	id := u.ID
	return &id
}

func (e *eoActionExecutor) executeNotification(ctx context.Context, action *models.TransitionAction, rec *models.EORecord, transition *models.WorkflowTransition, by *models.User) error {
	var cfg eoNotificationConfig
	if err := json.Unmarshal([]byte(action.Config), &cfg); err != nil {
		return fmt.Errorf("invalid notification config: %w", err)
	}
	to := e.resolveRecipients(ctx, cfg.Recipients, rec).Emails
	if len(to) == 0 {
		return nil
	}
	vars := eoActionVars(rec, transition, by)
	_, err := e.notifier.SendNotification(ctx, "notification", nil, "en", to, nil, nil,
		eoRender(cfg.Title, vars, nil), eoRender(cfg.Message, vars, nil), nil, nil, eoSentBy(by), nil)
	if err != nil {
		return fmt.Errorf("notification send failed: %w", err)
	}
	return nil
}

func (e *eoActionExecutor) executeEmail(ctx context.Context, action *models.TransitionAction, rec *models.EORecord, transition *models.WorkflowTransition, by *models.User) error {
	var cfg eoEmailConfig
	if err := json.Unmarshal([]byte(action.Config), &cfg); err != nil {
		return fmt.Errorf("invalid email config: %w", err)
	}
	to := e.resolveRecipients(ctx, cfg.Recipients, rec).Emails
	if len(to) == 0 {
		return nil
	}
	vars := eoActionVars(rec, transition, by)
	var tpl *string
	var subject, body string
	if cfg.TemplateCode != "" {
		tpl = &cfg.TemplateCode
	} else {
		subject, body = eoRender(cfg.SubjectTemplate, vars, nil), eoRender(cfg.BodyTemplate, vars, nil)
	}
	lang := cfg.Language
	if lang == "" {
		lang = "ar"
	}
	if _, err := e.notifier.SendNotification(ctx, "email", tpl, lang, to, nil, nil, subject, body, vars, nil, eoSentBy(by), nil); err != nil {
		return fmt.Errorf("email send failed: %w", err)
	}
	return nil
}

func (e *eoActionExecutor) executeSMS(ctx context.Context, action *models.TransitionAction, rec *models.EORecord, transition *models.WorkflowTransition, by *models.User) error {
	var cfg eoSMSConfig
	if err := json.Unmarshal([]byte(action.Config), &cfg); err != nil {
		return fmt.Errorf("invalid sms config: %w", err)
	}
	to := e.resolveRecipients(ctx, cfg.Recipients, rec).Phones
	if len(to) == 0 {
		return nil
	}
	vars := eoActionVars(rec, transition, by)
	var tpl *string
	var body string
	if cfg.TemplateCode != "" {
		tpl = &cfg.TemplateCode
	} else {
		body = eoRender(cfg.MessageTemplate, vars, nil)
	}
	lang := cfg.Language
	if lang == "" {
		lang = "ar"
	}
	if _, err := e.notifier.SendNotification(ctx, "sms", tpl, lang, to, nil, nil, "", body, vars, nil, eoSentBy(by), nil); err != nil {
		return fmt.Errorf("sms send failed: %w", err)
	}
	return nil
}

func (e *eoActionExecutor) executeWebhook(ctx context.Context, action *models.TransitionAction, rec *models.EORecord, transition *models.WorkflowTransition, by *models.User) error {
	var cfg eoWebhookConfig
	if err := json.Unmarshal([]byte(action.Config), &cfg); err != nil {
		return fmt.Errorf("invalid webhook config: %w", err)
	}
	if u, err := url.Parse(cfg.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("webhook url %q is not an absolute http(s) url", cfg.URL)
	}
	method := cfg.Method
	if method == "" {
		method = http.MethodPost
	}
	// Values are JSON-escaped: the body is JSON, and a subject containing a
	// quote must not break it.
	body := eoRender(cfg.Body, eoActionVars(rec, transition, by), eoJSONEscape)

	req, err := http.NewRequestWithContext(ctx, method, cfg.URL, bytes.NewBufferString(body))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned error status: %d", resp.StatusCode)
	}
	return nil
}

// ── field_update ────────────────────────────────────────────────────────────

type eoFieldUpdateConfig struct {
	Field string `json:"field"`
	Value any    `json:"value"`
}

// executeFieldUpdate writes one field through EORecordService.Update, not
// around it: that is what keeps an undefined key out of the values bag, a
// locked field locked, a value validated against its field kind and derived
// fields recomputed. It is not limited to two columns the way the incident
// side is — any field the record's type defines can be set.
func (e *eoActionExecutor) executeFieldUpdate(ctx context.Context, action *models.TransitionAction, rec *models.EORecord, by *models.User) error {
	var cfg eoFieldUpdateConfig
	if err := json.Unmarshal([]byte(action.Config), &cfg); err != nil {
		return fmt.Errorf("invalid field update config: %w", err)
	}
	if strings.TrimSpace(cfg.Field) == "" {
		return errors.New("field update config names no field")
	}
	_, err := e.recordSvc.Update(ctx, rec.ID, &models.EORecordUpdateRequest{
		RecordTypeID: rec.RecordTypeID,
		Values:       map[string]any{cfg.Field: cfg.Value},
	}, by)
	if err != nil {
		return fmt.Errorf("update field %q: %w", cfg.Field, err)
	}
	return nil
}

// ── spawn_record ────────────────────────────────────────────────────────────

type eoSpawnConfig struct {
	TargetType string `json:"target_type"`
	LinkType   string `json:"link_type"`
	// FieldMap is target field key -> value. A string that is exactly one
	// {{key}} takes the source record's value of that key with its own type;
	// a string with {{key}} inside is rendered as text; anything else is a
	// literal. A {{key}} whose source value is unset leaves the target field
	// unset rather than writing an empty one.
	FieldMap map[string]any `json:"field_map"`
}

var eoWholePlaceholderRE = regexp.MustCompile(`^\s*\{\{\s*([A-Za-z0-9_]+)\s*\}\}\s*$`)

// executeSpawnRecord creates a child record of the configured type, linked to
// this one, through EORecordService.Create — so the child gets its reference,
// its initial state, its link rules and its derived fields exactly as one
// created by hand.
//
// Idempotency. A transition must not spawn a second child when it is
// dispatched again, so before creating anything the executor looks for an
// existing direct child of this record of the target type, and spawns only if
// there is none. The check is by parent and type because the marker has to
// live somewhere that needs no schema: a child's values accept only keys its
// type defines. The cost is that a record which legitimately re-enters the
// transition does not spawn a second child while the first exists, and that
// two spawn_record actions to the same target type from one record collapse
// into one.
//
// A misconfigured action — unknown target type, no link type, a field-map key
// the target type does not define — spawns nothing, and says so twice: the
// error is logged, and a history entry is written on the PARENT naming the
// action and the reason, so the person looking at the record can see that the
// task they expected never appeared.
func (e *eoActionExecutor) executeSpawnRecord(ctx context.Context, action *models.TransitionAction, rec *models.EORecord, transition *models.WorkflowTransition, by *models.User) error {
	err := e.spawn(ctx, action, rec, transition, by)
	if err != nil {
		e.recordSpawnFailure(ctx, action, rec, transition, by, err)
	}
	return err
}

func (e *eoActionExecutor) spawn(ctx context.Context, action *models.TransitionAction, rec *models.EORecord, transition *models.WorkflowTransition, by *models.User) error {
	var cfg eoSpawnConfig
	if err := json.Unmarshal([]byte(action.Config), &cfg); err != nil {
		return fmt.Errorf("invalid spawn_record config: %w", err)
	}
	if strings.TrimSpace(cfg.TargetType) == "" {
		return errors.New("spawn_record config names no target_type")
	}
	if strings.TrimSpace(cfg.LinkType) == "" {
		return errors.New("spawn_record config names no link_type: the link type carries the rules that apply to the child")
	}
	target, err := e.types.GetByKey(ctx, cfg.TargetType)
	if err != nil {
		return fmt.Errorf("load target type %q: %w", cfg.TargetType, err)
	}
	if target == nil {
		return fmt.Errorf("target_type %q is not a record type", cfg.TargetType)
	}

	// Every key the map writes must be a field the target type defines.
	// Create would reject an undefined one anyway; checking first names every
	// offender at once and reaches the log before a reference is burned.
	defs, err := eoEffectiveFields(ctx, e.types, target)
	if err != nil {
		return fmt.Errorf("resolve fields of %q: %w", cfg.TargetType, err)
	}
	defined := make(map[string]bool, len(defs))
	for _, d := range defs {
		defined[d.Key] = true
	}
	var unknown []string
	for k := range cfg.FieldMap {
		if !defined[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("field_map writes %s, which %q does not define", strings.Join(unknown, ", "), cfg.TargetType)
	}

	existing, err := e.records.Children(ctx, rec.ID, &target.ID)
	if err != nil {
		return fmt.Errorf("look for an existing child: %w", err)
	}
	if len(existing) > 0 {
		log.Printf("eo: record %s: spawn_record %q skipped: already has a %q child (%s)", rec.Reference, action.Name, cfg.TargetType, existing[0].ID)
		return nil
	}

	source := eoFlattenStored(rec)
	vars := eoActionVars(rec, transition, by)
	values := make(map[string]any, len(cfg.FieldMap))
	for key, raw := range cfg.FieldMap {
		s, isString := raw.(string)
		if !isString {
			values[key] = raw
			continue
		}
		if m := eoWholePlaceholderRE.FindStringSubmatch(s); m != nil {
			if v := eoPlain(source[m[1]]); v != nil {
				values[key] = v
			}
			continue
		}
		values[key] = eoRender(s, vars, nil)
	}

	parentID := rec.ID
	if _, err := e.recordSvc.Create(ctx, &models.EORecordCreateRequest{
		RecordTypeID:      target.ID,
		ParentRecordID:    &parentID,
		ParentLinkTypeKey: cfg.LinkType,
		Values:            values,
	}, by); err != nil {
		return fmt.Errorf("create %q record: %w", cfg.TargetType, err)
	}
	return nil
}

func (e *eoActionExecutor) recordSpawnFailure(ctx context.Context, action *models.TransitionAction, rec *models.EORecord, transition *models.WorkflowTransition, by *models.User, cause error) {
	var tid *uuid.UUID
	if transition != nil {
		id := transition.ID
		tid = &id
	}
	err := e.records.CreateHistory(ctx, &models.EORecordHistory{
		RecordID:       rec.ID,
		RecordTypeID:   rec.RecordTypeID,
		TransitionID:   tid,
		PerformedByID:  eoSentBy(by),
		Comment:        fmt.Sprintf("spawn_record action %q did not create a record: %v", action.Name, cause),
		OldValues:      datatypes.JSON(`{}`),
		NewValues:      datatypes.JSON(`{}`),
		IsSystemAction: true,
	})
	if err != nil {
		log.Printf("eo: record %s: could not record the failed spawn_record in its history: %v", rec.Reference, err)
	}
}
