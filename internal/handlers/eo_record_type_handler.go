package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/i18n"
	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// EORecordTypeHandler is the designer: record types, their field definitions
// and the link types between them (spec §7.2, §7.3, §7.6). Defining a type is
// behind eo:types:manage; READING the catalogue is behind eo:view, because
// every EO screen is generated from it (see RegisterRoutes).
//
// It holds the RECORD repository as well as the type repository because the
// guard rails of spec §7.14 are conditional on one fact only the register
// knows: whether the type already has records.
type EORecordTypeHandler struct {
	types   repository.EORecordTypeRepository
	records repository.EORecordRepository
}

func NewEORecordTypeHandler(types repository.EORecordTypeRepository, records repository.EORecordRepository) *EORecordTypeHandler {
	return &EORecordTypeHandler{types: types, records: records}
}

// RegisterRoutes registers the designer routes on r. Every literal sub-path
// (/link-types and everything under it) registers before /:id, because Fiber
// matches in registration order and /eo-record-types/link-types would
// otherwise resolve to the by-id handler.
func (h *EORecordTypeHandler) RegisterRoutes(r fiber.Router, perm EOPermissionMiddleware) {
	// The catalogue READS are not a designer privilege. Every EO screen is
	// generated FROM the catalogue — a register list needs its type's name,
	// and a form is built from that type's field definitions — so a user who
	// may read records at all must be able to read the types, or the module
	// renders empty. Defining the types remains eo:types:manage.
	//
	// THAT INCLUDES GET /:id/fields. An earlier wording claimed the route was
	// "deliberately NOT here" because "the field definitions the UI renders a
	// form from arrive inside Get" — an assertion about a client in another
	// repository, which this file cannot see and must not make. What is
	// verifiable here is the server side of it: Get returns t.Fields and List
	// does not, so a form built for a type AND ITS ANCESTORS needs either one
	// Get per type or this route, and nothing in this package can say which a
	// client chose. Gating it on eo:types:manage made the choice for the
	// client, and for the pilot operator role (executive_office, which holds
	// eo:view and not eo:types:manage) it made the create form unrenderable.
	//
	// It is not a confidentiality boundary either way: GET /:id already
	// returns the same field definitions to the same eo:view caller, so the
	// widening tells nobody anything new. The three field MUTATIONS stay on
	// eo:types:manage, which is where the designer privilege actually lives.
	const (
		read   = "eo:view"
		manage = "eo:types:manage"
	)

	r.Get("/link-types", perm(read), h.ListLinkTypes)
	r.Post("/link-types", perm(manage), h.CreateLinkType)
	r.Put("/link-types/:link_type_id", perm(manage), h.UpdateLinkType)
	r.Delete("/link-types/:link_type_id", perm(manage), h.DeleteLinkType)

	r.Get("/", perm(read), h.List)
	r.Post("/", perm(manage), h.Create)
	r.Get("/:id", perm(read), h.Get)
	r.Put("/:id", perm(manage), h.Update)
	r.Delete("/:id", perm(manage), h.Delete)

	r.Get("/:id/fields", perm(read), h.ListFields)
	r.Post("/:id/fields", perm(manage), h.CreateField)
	r.Put("/:id/fields/:field_id", perm(manage), h.UpdateField)
	r.Delete("/:id/fields/:field_id", perm(manage), h.DeleteField)
}

// ── request shapes ──────────────────────────────────────────────────────────

// eoRecordTypeRequest is the editable attribute set of a record type. PUT
// REPLACES that set: an omitted key is an empty value, not "leave it alone".
// That is the safer reading here, because the two attributes a partial update
// would most plausibly drop — reference_prefix and reference_pattern — are the
// two ValidateTypeEdit refuses to see change while records exist, so a
// dropped key is refused rather than silently applied.
//
// `capabilities` IS THE ONE EXCEPTION, and applyTo below is where it is made:
// an omitted or empty `capabilities` leaves the stored column alone. THAT IS
// LOAD-BEARING, not a convenience. It means the only way to express "this
// type enables no capabilities" is to send `[]` explicitly, and the pilot
// seed reads an empty column plus the absence of a marker row as "never
// configured" and converges it — see database.eoCapabilitiesNeverConfigured,
// which records what the marker closes and the one upgrade boot it does not.
type eoRecordTypeRequest struct {
	Key              string         `json:"key"`
	Name             string         `json:"name"`
	NameAr           string         `json:"name_ar"`
	Icon             string         `json:"icon"`
	ParentTypeID     *uuid.UUID     `json:"parent_type_id"`
	IsAbstract       bool           `json:"is_abstract"`
	ReferencePrefix  string         `json:"reference_prefix"`
	ReferenceMode    string         `json:"reference_mode"`
	ReferencePattern string         `json:"reference_pattern"`
	WorkflowID       *uuid.UUID     `json:"workflow_id"`
	Capabilities     datatypes.JSON `json:"capabilities"`
	IsActive         *bool          `json:"is_active"`
	// ConfirmDestructive is spec §7.14's other half: the operator's explicit
	// authorisation of ONE destructive change, with the backfill rule they
	// state for the records it leaves behind. It is not an attribute of the
	// type and applyTo never writes it anywhere; it is consulted by the guard
	// rail and recorded in the audit row.
	ConfirmDestructive *models.EODestructiveConfirmation `json:"confirm_destructive"`
}

// applyTo writes the request onto t, which is either a fresh type or the
// stored one. It never touches ID, PartitionPending or the timestamps: the
// partition flag is the migration tooling's, not the designer's.
func (r *eoRecordTypeRequest) applyTo(t *models.EORecordType) {
	t.Key = strings.TrimSpace(r.Key)
	t.Name = strings.TrimSpace(r.Name)
	t.NameAr = strings.TrimSpace(r.NameAr)
	t.Icon = strings.TrimSpace(r.Icon)
	t.ParentTypeID = r.ParentTypeID
	t.IsAbstract = r.IsAbstract
	t.ReferencePrefix = strings.TrimSpace(r.ReferencePrefix)
	t.ReferenceMode = strings.TrimSpace(r.ReferenceMode)
	t.ReferencePattern = strings.TrimSpace(r.ReferencePattern)
	t.WorkflowID = r.WorkflowID
	if len(r.Capabilities) > 0 {
		t.Capabilities = r.Capabilities
	}
	if r.IsActive != nil {
		t.IsActive = *r.IsActive
	}
}

// eoFieldDefRequest is the editable attribute set of one field definition,
// with the same replace-on-PUT reading.
type eoFieldDefRequest struct {
	Key                  string         `json:"key"`
	Label                string         `json:"label"`
	LabelAr              string         `json:"label_ar"`
	Kind                 string         `json:"kind"`
	Config               datatypes.JSON `json:"config"`
	Provenance           string         `json:"provenance"`
	RequiredMode         string         `json:"required_mode"`
	RequiredRule         string         `json:"required_rule"`
	RequiredAtStateIDs   datatypes.JSON `json:"required_at_state_ids"`
	Formula              string         `json:"formula"`
	RecomputeOn          datatypes.JSON `json:"recompute_on"`
	IsIndexed            bool           `json:"is_indexed"`
	IsLockedAfterStateID *uuid.UUID     `json:"is_locked_after_state_id"`
	SortOrder            int            `json:"sort_order"`
	HelpText             string         `json:"help_text"`
	HelpTextAr           string         `json:"help_text_ar"`
	// See eoRecordTypeRequest.ConfirmDestructive. On the three field routes
	// it authorises removing a field, changing its kind or making it
	// required — the three §7.14 rules a field edit can break.
	ConfirmDestructive *models.EODestructiveConfirmation `json:"confirm_destructive"`
}

// eoFieldDeleteRequest is the only thing a DELETE body may carry. It is a
// type of its own rather than eoFieldDefRequest so that a client cannot send
// an attribute set to a route that applies none of it.
type eoFieldDeleteRequest struct {
	ConfirmDestructive *models.EODestructiveConfirmation `json:"confirm_destructive"`
}

func (r *eoFieldDefRequest) applyTo(f *models.EOFieldDef) {
	f.Key = strings.TrimSpace(r.Key)
	f.Label = strings.TrimSpace(r.Label)
	f.LabelAr = strings.TrimSpace(r.LabelAr)
	f.Kind = strings.TrimSpace(r.Kind)
	if len(r.Config) > 0 {
		f.Config = r.Config
	}
	f.Provenance = strings.TrimSpace(r.Provenance)
	f.RequiredMode = strings.TrimSpace(r.RequiredMode)
	f.RequiredRule = r.RequiredRule
	if len(r.RequiredAtStateIDs) > 0 {
		f.RequiredAtStateIDs = r.RequiredAtStateIDs
	}
	f.Formula = r.Formula
	if len(r.RecomputeOn) > 0 {
		f.RecomputeOn = r.RecomputeOn
	}
	f.IsIndexed = r.IsIndexed
	f.IsLockedAfterStateID = r.IsLockedAfterStateID
	f.SortOrder = r.SortOrder
	f.HelpText = strings.TrimSpace(r.HelpText)
	f.HelpTextAr = strings.TrimSpace(r.HelpTextAr)
}

// ── the guard rails ─────────────────────────────────────────────────────────

// eoGuardTypeEdit is models.ValidateTypeEdit's ONE caller. Every mutation of a
// type or of its field definitions goes through it, because §7.14's rules
// (remove a field, change its kind, make it required, change the reference
// prefix or pattern) are stated over a (type, fields) pair and a field route
// changes exactly that pair.
//
// It answers the request itself when the edit is refused and reports that as
// allowed=false, so a caller writes
//
//	allowed, overridden, err := h.eoGuardTypeEdit(...)
//	if !allowed {
//		return err
//	}
//
// and can no longer reach its repository write. The bool is load-bearing:
// utils.ErrorResponse returns nil on a successful write, so a refusal handed
// back in the error slot alone would read as "edit allowed" at every call
// site and the destructive write would go through under a 400 response.
//
// `overridden` is the §7.14 refusal the confirmation authorised, empty when
// there was nothing to confirm. It is what the audit row records, and it is
// also the signal that an audit row is owed at all.
//
// IT ASKS THE MODEL TWICE, DELIBERATELY. The first call is the question §7.14
// asks of every edit, with no confirmation, and its answer is the sentence the
// operator would have been refused with. The second decides whether the
// confirmation supplied authorises that edit. The DIFFERENCE between the two
// answers is precisely "the confirmation was used" — which is the only thing
// that may write an override into the audit log, and is not derivable from
// "the request carried a confirmation": a client that attaches the operator's
// last confirmation to every save would otherwise record every save as a
// confirmed destruction. Both calls are pure and cheap.
func (h *EORecordTypeHandler) eoGuardTypeEdit(
	c *fiber.Ctx,
	before, after models.EORecordType,
	fieldsBefore, fieldsAfter []models.EOFieldDef,
	confirm *models.EODestructiveConfirmation,
) (bool, string, error) {
	hasRecords, err := h.eoTypeHasRecords(c, before.ID)
	if err != nil {
		return false, "", utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	refusal := models.ValidateTypeEdit(before, after, fieldsBefore, fieldsAfter, hasRecords, nil)
	if err := models.ValidateTypeEdit(before, after, fieldsBefore, fieldsAfter, hasRecords, confirm); err != nil {
		// The message names the field and the reason; it is the whole point
		// of the guard rail and is returned verbatim rather than flattened
		// into a generic "invalid". With a confirmation that does not match,
		// it also names what the confirmation did authorise.
		var destructive *models.EODestructiveRefusal
		if errors.As(err, &destructive) {
			return false, "", eoDestructiveRefusalResponse(c, destructive)
		}
		return false, "", utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	if refusal == nil {
		return true, "", nil
	}
	return true, refusal.Error(), nil
}

// eoDestructiveRefusalBody is a §7.14 refusal on the wire. `error` is the
// English sentence, unchanged: the client's refusal banner reads it. The
// message_* fields are the same refusal as an i18n key plus the named values
// the sentence interpolates, for a client rendering it in the reader's
// language. message_detail_* is the sentence's optional second half (a
// confirmation that does not match, or several destructive changes at once).
type eoDestructiveRefusalBody struct {
	Success           bool              `json:"success"`
	Error             string            `json:"error"`
	MessageKey        string            `json:"message_key"`
	MessageArgs       map[string]string `json:"message_args"`
	MessageDetailKey  string            `json:"message_detail_key,omitempty"`
	MessageDetailArgs map[string]string `json:"message_detail_args,omitempty"`
}

func eoDestructiveRefusalResponse(c *fiber.Ctx, r *models.EODestructiveRefusal) error {
	// Recorded for the Error Log exactly as utils.ErrorResponse would.
	c.Locals(utils.LocalErrorMessage, r.Message)
	return c.Status(fiber.StatusBadRequest).JSON(eoDestructiveRefusalBody{
		Success:           false,
		Error:             r.Message,
		MessageKey:        r.Key,
		MessageArgs:       r.Args,
		MessageDetailKey:  r.DetailKey,
		MessageDetailArgs: r.DetailArgs,
	})
}

// ── the audit row a confirmed destructive edit owes ─────────────────────────

// eoTypeEditAuditModule and eoTypeEditAuditAction are how the row is found
// again. The action log's filter options are SELECT DISTINCT over the stored
// rows (repository.actionLogRepository.GetDistinctModules/Actions), so a pair
// of values of its own appears in the existing action-log screen by itself and
// needs no registration anywhere.
const (
	eoTypeEditAuditModule = "eo_type_designer"
	eoTypeEditAuditAction = "confirm_destructive_edit"
)

// eoTypeEditAuditDetail is what the row's new_value holds: the confirmation as
// it arrived, plus the refusal it overrode. The refusal is the load-bearing
// half — "an administrator confirmed a destructive edit" is not an audit of
// what was destroyed, and §7.14's whole demand is that this never happens
// silently.
//
// WHAT middleware.ActionLogger ADDS, AND WHY IT IS STILL ON THIS GROUP (see
// cmd/server/main.go, where it wraps /admin/eo-record-types). It logs the
// CALL — including one that never reaches this function, such as a refused
// confirmation — but it captures no request body, so it can say that somebody
// PUT this field definition and not which change they authorised or why. The
// two entries are complementary and neither replaces the other.
type eoTypeEditAuditDetail struct {
	TypeKey  string `json:"type_key"`
	FieldKey string `json:"field_key"`
	Change   string `json:"change"`
	Reason   string `json:"reason"`
	Refusal  string `json:"refusal"`
}

// eoConfirmedEditAudit builds the row a confirmed edit owes, or (nil, true,
// nil) when the edit overrode nothing. It follows the eoGuardTypeEdit calling
// convention: ok=false means the request has been answered.
//
// AN UNATTRIBUTED OVERRIDE IS REFUSED. The row's whole value is that it names
// who authorised the destruction, so a request that reaches here without an
// actor is answered 401 rather than audited to nobody. The designer routes run
// behind the auth middleware in cmd/server/main.go, so that arm is not
// reachable through the registered route set; it is kept because eoActor is
// the only thing in this package that establishes the fact, and a route group
// assembled differently — as the tests assemble one — would otherwise write an
// audit row with a zero user id.
func (h *EORecordTypeHandler) eoConfirmedEditAudit(
	c *fiber.Ctx,
	t models.EORecordType,
	confirm *models.EODestructiveConfirmation,
	overridden string,
) (*models.ActionLog, bool, error) {
	if overridden == "" {
		return nil, true, nil
	}
	actor, ok, err := eoActor(c)
	if !ok {
		return nil, false, err
	}
	detail, marshalErr := json.Marshal(eoTypeEditAuditDetail{
		TypeKey:  confirm.TypeKey,
		FieldKey: confirm.FieldKey,
		Change:   confirm.Change,
		Reason:   confirm.Reason,
		Refusal:  overridden,
	})
	if marshalErr != nil {
		return nil, false, utils.InternalErrorResponse(c, marshalErr, i18n.T(c.UserContext(), "internal_server_error"))
	}
	// BOTH ARE TRUNCATED, AND BOTH ARE CLIENT-CONTROLLED. action_logs
	// .user_agent is varchar(500) and .ip_address is varchar(45); a request
	// with a 600-character User-Agent header fails the INSERT with SQLSTATE
	// 22001, and because that INSERT shares the edit's transaction it rolls
	// the confirmed destructive edit back with an opaque 500. The IP is
	// within 45 only because no ProxyHeader is configured today
	// (cmd/server/main.go) — configuring one makes c.IP() return the whole
	// X-Forwarded-For chain, which a client writes.
	ip, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	agent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)
	ip, agent = eoTruncate(ip, 45), eoTruncate(agent, 500)
	where := "the record type itself"
	if confirm.FieldKey != "" {
		where = fmt.Sprintf("field %q", confirm.FieldKey)
	}
	return &models.ActionLog{
		UserID:     actor.ID,
		Action:     eoTypeEditAuditAction,
		Module:     eoTypeEditAuditModule,
		ResourceID: t.ID.String(),
		Description: eoTruncate(fmt.Sprintf("Confirmed a destructive edit (%s) to %s on EO record type %q, overriding spec §7.14. Stated backfill rule: %s",
			confirm.Change, where, t.Key, confirm.Reason), 500),
		NewValue:  string(detail),
		IPAddress: ip,
		UserAgent: agent,
		Status:    "success",
		CreatedAt: time.Now(),
	}, true, nil
}

// eoTruncate bounds a string to n CHARACTERS, which is what Postgres counts
// for varchar(n). models.ActionLog.Description is varchar(500) and the stated
// rule is free text, so without this the audit INSERT could fail on a value
// too long for its column — and because that INSERT shares the edit's
// transaction, it would roll the edit back with an internal error instead of
// recording it. The model caps the rule itself; this caps the sentence built
// around it.
func eoTruncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	const ellipsis = "…"
	return string(runes[:n-1]) + ellipsis
}

// eoWriteTypeEdit applies one designer write, and when the edit was authorised
// by a confirmation, writes the audit row IN THE SAME TRANSACTION.
//
// An ordinary edit takes the repository directly and opens no transaction:
// there is no second write to be atomic with, and the single-statement writes
// are already atomic by themselves.
func (h *EORecordTypeHandler) eoWriteTypeEdit(c *fiber.Ctx, audit *models.ActionLog, write func(repository.EORecordTypeRepository) error) error {
	if audit == nil {
		return write(h.types)
	}
	return h.types.Transaction(c.UserContext(), func(tx repository.EORecordTypeRepository) error {
		if err := write(tx); err != nil {
			return err
		}
		return tx.WriteActionLog(c.UserContext(), audit)
	})
}

// eoTypeHasRecords asks the register, under an admin scope: the question is
// "does this type hold anything", not "does the caller see anything", and a
// visibility-scoped count would let a restricted record be destroyed by an
// administrator who cannot see it.
//
// It asks about the type AND EVERY TYPE BENEATH IT. Asking only about records
// whose record_type_id IS this type makes every §7.14 rule void for exactly
// the types the rules matter most on: an ABSTRACT base type holds no records
// of its own by definition, so that count is always zero, and removing an
// inherited field, changing its kind, making it required, re-keying,
// re-parenting or repointing the workflow would all be waved through on
// eo_base and on every intermediate ancestor — however many records their
// descendants hold. The stored values such an edit orphans live on the
// DESCENDANTS' records, so the descendants' records are the question.
//
// The subtree comes from the type repository and the count from the record
// repository: no SQL is written here, so the register's partitioning and its
// visibility clause stay in the one place that owns them.
func (h *EORecordTypeHandler) eoTypeHasRecords(c *fiber.Ctx, typeID uuid.UUID) (bool, error) {
	subtree, err := h.types.DescendantIDs(c.UserContext(), typeID)
	if err != nil {
		return false, err
	}
	ids := make([]string, 0, len(subtree))
	for _, id := range subtree {
		ids = append(ids, id.String())
	}
	_, total, err := h.records.List(c.UserContext(), &models.EORecordFilter{
		RecordTypeID: ids,
		IsAdmin:      true,
		Limit:        1,
	})
	if err != nil {
		return false, err
	}
	return total > 0, nil
}

// eoCheckReferencePrefix applies models.ValidateReferencePrefix against every
// other configured type. Global reference uniqueness rests entirely on
// prefixes not overlapping, because `reference` is unique per PARTITION only.
//
// An abstract type holds no records of its own and so needs no prefix, and a
// parent-derived one issues every reference inside its parent's. The rule
// itself exempts both; they are short-circuited here as well so neither pays
// for the List below.
func (h *EORecordTypeHandler) eoCheckReferencePrefix(c *fiber.Ctx, t *models.EORecordType) (bool, error) {
	if t.IsAbstract || t.ReferenceMode == models.EOReferenceModeParent {
		return true, nil
	}
	all, err := h.types.List(c.UserContext())
	if err != nil {
		return false, utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	others := make([]models.EORecordType, 0, len(all))
	for _, other := range all {
		if other.ID != t.ID {
			others = append(others, other)
		}
	}
	if err := models.ValidateReferencePrefix(*t, others); err != nil {
		return false, eoFieldErrors(c, map[string]string{"reference_prefix": err.Error()})
	}
	return true, nil
}

// eoLoadType reads a type or answers 404.
func (h *EORecordTypeHandler) eoLoadType(c *fiber.Ctx, id uuid.UUID) (*models.EORecordType, bool, error) {
	t, err := h.types.GetByID(c.UserContext(), id)
	if err != nil {
		return nil, false, utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if t == nil {
		return nil, false, utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "eo_record_type_not_found"))
	}
	return t, true, nil
}

// eoOwnFields reads a type's OWN field definitions — not the inherited ones.
// §7.14 is stated over what the type declares: an ancestor's field is not this
// type's to remove or re-kind.
func (h *EORecordTypeHandler) eoOwnFields(c *fiber.Ctx, typeID uuid.UUID) ([]models.EOFieldDef, bool, error) {
	defs, err := h.types.FieldDefs(c.UserContext(), []uuid.UUID{typeID})
	if err != nil {
		return nil, false, utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	return defs, true, nil
}

// ── record types ────────────────────────────────────────────────────────────

func (h *EORecordTypeHandler) List(c *fiber.Ctx) error {
	out, err := h.types.List(c.UserContext())
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if out == nil {
		out = []models.EORecordType{}
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_record_types_retrieved"), out)
}

func (h *EORecordTypeHandler) Get(c *fiber.Ctx) error {
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	t, ok, err := h.eoLoadType(c, id)
	if !ok {
		return err
	}
	fields, ok, err := h.eoOwnFields(c, id)
	if !ok {
		return err
	}
	t.Fields = fields
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_record_type_retrieved"), t)
}

// eoTypeWriteError answers a metamodel write that failed, naming the two
// causes an operator can act on.
//
// Both come from the per-type permission codes the write carries (spec
// §7.11), and both used to arrive as a bare 500 with the cause in a log the
// operator cannot read — "Internal server error" and a dead designer screen.
//
//   - ErrEOTypePermissionCodesExist is the operator's to resolve and names a
//     bad KEY, so it is answered as a field error on `key`, which is where
//     the form shows it and where the decision is made.
//   - ErrEOTypePermissionRoleMissing is a broken deployment, not a bad
//     request: still a 500, still logged, but it says which roles to restore.
func eoTypeWriteError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, repository.ErrEOTypePermissionCodesExist):
		return eoFieldErrors(c, map[string]string{
			"key": fmt.Sprintf(i18n.T(c.UserContext(), "eo_record_type_key_has_permissions"), eoCodesFromError(err)),
		})
	case errors.Is(err, repository.ErrEOTypePermissionRoleMissing):
		return utils.InternalErrorResponse(c, err,
			fmt.Sprintf(i18n.T(c.UserContext(), "eo_record_type_role_missing"), strings.Join(models.EOTypePermissionRoleCodes, ", ")))
	default:
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
}

// eoCodesFromError lifts the comma-separated code list the repository appends
// after the sentinel, so the operator is told WHICH codes are in the way
// rather than being sent to look for them.
func eoCodesFromError(err error) string {
	msg := err.Error()
	marker := repository.ErrEOTypePermissionCodesExist.Error() + ": "
	if i := strings.Index(msg, marker); i >= 0 {
		return msg[i+len(marker):]
	}
	return msg
}

func (h *EORecordTypeHandler) Create(c *fiber.Ctx) error {
	if _, actorOK, actorErr := eoActor(c); !actorOK {
		return actorErr
	}
	var req eoRecordTypeRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	t := &models.EORecordType{ID: uuid.New(), IsActive: true, PartitionPending: true}
	req.applyTo(t)
	if fieldErrs := eoValidateTypeShape(c, t); len(fieldErrs) > 0 {
		return eoFieldErrors(c, fieldErrs)
	}
	if allowed, err := h.eoCheckReferencePrefix(c, t); !allowed {
		return err
	}
	// No ValidateTypeEdit here: a brand-new type has no records, and §7.14 is
	// a rule about changing a type that does.
	if err := h.types.Create(c.UserContext(), t); err != nil {
		return eoTypeWriteError(c, err)
	}
	return utils.SuccessResponse(c, fiber.StatusCreated, i18n.T(c.UserContext(), "eo_record_type_created"), t)
}

func (h *EORecordTypeHandler) Update(c *fiber.Ctx) error {
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	var req eoRecordTypeRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	stored, ok, err := h.eoLoadType(c, id)
	if !ok {
		return err
	}
	before := *stored
	after := *stored
	req.applyTo(&after)
	if fieldErrs := eoValidateTypeShape(c, &after); len(fieldErrs) > 0 {
		return eoFieldErrors(c, fieldErrs)
	}
	fields, ok, err := h.eoOwnFields(c, id)
	if !ok {
		return err
	}
	// The guard rail runs BEFORE the write, on the proposed after-state. The
	// field definitions are unchanged by this route, so they are passed as
	// both sides: what §7.14 judges here is the reference prefix and pattern,
	// the workflow, the parent, the key and the abstract flag.
	allowed, overridden, err := h.eoGuardTypeEdit(c, before, after, fields, fields, req.ConfirmDestructive)
	if !allowed {
		return err
	}
	if allowed, err := h.eoCheckReferencePrefix(c, &after); !allowed {
		return err
	}
	// The audit row names the type as it is BEFORE the edit, which is the
	// name the confirmation had to use and the only one the stored rows and
	// the operator agree on when a re-key is what is being confirmed.
	audit, ok, err := h.eoConfirmedEditAudit(c, before, req.ConfirmDestructive, overridden)
	if !ok {
		return err
	}
	if err := h.eoWriteTypeEdit(c, audit, func(r repository.EORecordTypeRepository) error {
		return r.Update(c.UserContext(), &after)
	}); err != nil {
		return eoTypeWriteError(c, err)
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_record_type_updated"), &after)
}

// Delete soft-deletes a record type, and REFUSES while it holds records. A
// type is the partition key of the register: deleting one whose records exist
// would orphan a statutory audit trail behind a configuration row nobody can
// read any more. Removing those records is not this endpoint's to decide.
//
// It also refuses while any other type names this one as its parent_type_id,
// REGARDLESS of records. A soft-deleted parent is invisible to
// EORecordTypeRepository.GetByID, so Ancestors answers "parent … not found"
// for every descendant, and every create, update and transition of every
// descendant type fails until someone un-deletes the row by hand in SQL. A
// type with children is not deletable; the children are re-parented or
// removed first.
func (h *EORecordTypeHandler) Delete(c *fiber.Ctx) error {
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	if _, loaded, loadErr := h.eoLoadType(c, id); !loaded {
		return loadErr
	}
	children, err := h.types.ChildTypes(c.UserContext(), id)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if len(children) > 0 {
		keys := make([]string, 0, len(children))
		for _, child := range children {
			keys = append(keys, child.Key)
		}
		return utils.ErrorResponse(c, fiber.StatusConflict,
			i18n.Tf(c.UserContext(), "eo_record_type_has_children", strings.Join(keys, ", ")))
	}
	hasRecords, err := h.eoTypeHasRecords(c, id)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if hasRecords {
		return utils.ErrorResponse(c, fiber.StatusConflict, i18n.T(c.UserContext(), "eo_record_type_has_records"))
	}
	if err := h.types.Delete(c.UserContext(), id); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_record_type_deleted"), nil)
}

// eoValidateTypeShape checks what the model itself declares NOT NULL or
// constant-bounded, before anything reaches the database.
func eoValidateTypeShape(c *fiber.Ctx, t *models.EORecordType) map[string]string {
	errs := map[string]string{}
	if t.Key == "" {
		errs["key"] = i18n.T(c.UserContext(), "eo_key_required")
	}
	if t.Name == "" {
		errs["name"] = i18n.T(c.UserContext(), "eo_name_required")
	}
	switch t.ReferenceMode {
	case "", models.EOReferenceModeOwn, models.EOReferenceModeParent:
	default:
		errs["reference_mode"] = i18n.T(c.UserContext(), "eo_reference_mode_invalid")
	}
	// A concrete type cannot issue a reference without a pattern, and a
	// record whose reference cannot be rendered cannot be created at all.
	if !t.IsAbstract && t.ReferencePattern == "" {
		errs["reference_pattern"] = i18n.T(c.UserContext(), "eo_reference_pattern_required")
	}
	return errs
}

// ── field definitions ───────────────────────────────────────────────────────

func (h *EORecordTypeHandler) ListFields(c *fiber.Ctx) error {
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	if _, loaded, loadErr := h.eoLoadType(c, id); !loaded {
		return loadErr
	}
	fields, ok, err := h.eoOwnFields(c, id)
	if !ok {
		return err
	}
	if fields == nil {
		fields = []models.EOFieldDef{}
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_field_defs_retrieved"), fields)
}

func (h *EORecordTypeHandler) CreateField(c *fiber.Ctx) error {
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	var req eoFieldDefRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	t, ok, err := h.eoLoadType(c, id)
	if !ok {
		return err
	}
	before, ok, err := h.eoOwnFields(c, id)
	if !ok {
		return err
	}
	def := &models.EOFieldDef{ID: uuid.New(), RecordTypeID: id}
	req.applyTo(def)
	if fieldErrs := eoValidateFieldShape(c, def); len(fieldErrs) > 0 {
		return eoFieldErrors(c, fieldErrs)
	}
	// The collision check is against this type's OWN fields only, and that is
	// deliberate: a key an ANCESTOR declares is not a collision, it is an
	// override. services.ResolveEffectiveFields (eo_fields.go:20-30) states
	// the rule — "if a type redefines an ancestor's field key, the type's
	// definition replaces the ancestor's at the ancestor's position; the key
	// appears once" — and overriding is how a child narrows an inherited
	// field (optional to required, a relabel). Rejecting inherited keys here
	// would make that documented feature unreachable from the designer. What
	// is refused is two definitions of the same key on the SAME type, which
	// the unique index idx_eo_field_defs_type_key also forbids.
	for _, existing := range before {
		if existing.Key == def.Key {
			return eoFieldErrors(c, map[string]string{"key": i18n.T(c.UserContext(), "eo_field_key_exists")})
		}
	}
	after := append(append([]models.EOFieldDef{}, before...), *def)
	// Adding a field is additive and §7.14 allows it unconditionally — but it
	// is asked anyway, so that "every field change is judged" has no
	// exception a later edit could widen (adding a REQUIRED field to a type
	// with records is the case that matters).
	allowed, overridden, err := h.eoGuardTypeEdit(c, *t, *t, before, after, req.ConfirmDestructive)
	if !allowed {
		return err
	}
	// THE AUDIT ARM HERE CANNOT FIRE TODAY, and it is kept rather than
	// dropped. `overridden` is non-empty only when ValidateTypeEdit refuses
	// the edit without a confirmation, and this route's before-state is this
	// route's after-state minus one ADDED definition: no field is removed,
	// re-kinded or tightened, and the type itself is passed unchanged on both
	// sides, so there is nothing for the model to refuse. It is kept because
	// the alternative is to discard `overridden` here — and then the day a
	// rule makes some addition destructive (a required field on a populated
	// type is the standing candidate), this route would apply it with no
	// audit row and nothing would notice. A malformed confirmation is still
	// refused by the guard above, which is why a client may send one here.
	audit, ok, err := h.eoConfirmedEditAudit(c, *t, req.ConfirmDestructive, overridden)
	if !ok {
		return err
	}
	if err := h.eoWriteTypeEdit(c, audit, func(r repository.EORecordTypeRepository) error {
		return r.CreateFieldDef(c.UserContext(), def)
	}); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	return utils.SuccessResponse(c, fiber.StatusCreated, i18n.T(c.UserContext(), "eo_field_def_created"), def)
}

func (h *EORecordTypeHandler) UpdateField(c *fiber.Ctx) error {
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	fieldID, ok, err := eoParamUUID(c, "field_id")
	if !ok {
		return err
	}
	var req eoFieldDefRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	t, ok, err := h.eoLoadType(c, id)
	if !ok {
		return err
	}
	before, ok, err := h.eoOwnFields(c, id)
	if !ok {
		return err
	}
	stored, err := h.types.GetFieldDef(c.UserContext(), fieldID)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if stored == nil || stored.RecordTypeID != id {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "eo_field_def_not_found"))
	}
	updated := *stored
	req.applyTo(&updated)
	if fieldErrs := eoValidateFieldShape(c, &updated); len(fieldErrs) > 0 {
		return eoFieldErrors(c, fieldErrs)
	}
	after := make([]models.EOFieldDef, 0, len(before))
	for _, f := range before {
		if f.ID == fieldID {
			after = append(after, updated)
			continue
		}
		after = append(after, f)
	}
	allowed, overridden, err := h.eoGuardTypeEdit(c, *t, *t, before, after, req.ConfirmDestructive)
	if !allowed {
		return err
	}
	audit, ok, err := h.eoConfirmedEditAudit(c, *t, req.ConfirmDestructive, overridden)
	if !ok {
		return err
	}
	if err := h.eoWriteTypeEdit(c, audit, func(r repository.EORecordTypeRepository) error {
		return r.UpdateFieldDef(c.UserContext(), &updated)
	}); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_field_def_updated"), &updated)
}

func (h *EORecordTypeHandler) DeleteField(c *fiber.Ctx) error {
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	fieldID, ok, err := eoParamUUID(c, "field_id")
	if !ok {
		return err
	}
	// A DELETE carries no attribute set, so §7.14's confirmation arrives in
	// an OPTIONAL body. An absent body is the ordinary unconfirmed removal
	// and is parsed as nothing; a body that is present and unparseable is a
	// malformed request and is refused rather than silently read as "no
	// confirmation", which would answer the operator who typed one with the
	// refusal their confirmation was meant to resolve.
	var confirm *models.EODestructiveConfirmation
	if len(c.Body()) > 0 {
		var req eoFieldDeleteRequest
		if err := c.BodyParser(&req); err != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
		}
		confirm = req.ConfirmDestructive
	}
	t, ok, err := h.eoLoadType(c, id)
	if !ok {
		return err
	}
	before, ok, err := h.eoOwnFields(c, id)
	if !ok {
		return err
	}
	found := false
	after := make([]models.EOFieldDef, 0, len(before))
	for _, f := range before {
		if f.ID == fieldID {
			found = true
			continue
		}
		after = append(after, f)
	}
	if !found {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "eo_field_def_not_found"))
	}
	// This is the case §7.14 exists for: removing a field while records hold
	// values under its key.
	allowed, overridden, err := h.eoGuardTypeEdit(c, *t, *t, before, after, confirm)
	if !allowed {
		return err
	}
	audit, ok, err := h.eoConfirmedEditAudit(c, *t, confirm, overridden)
	if !ok {
		return err
	}
	if err := h.eoWriteTypeEdit(c, audit, func(r repository.EORecordTypeRepository) error {
		return r.DeleteFieldDef(c.UserContext(), fieldID)
	}); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_field_def_deleted"), nil)
}

func eoValidateFieldShape(c *fiber.Ctx, f *models.EOFieldDef) map[string]string {
	errs := map[string]string{}
	if f.Key == "" {
		errs["key"] = i18n.T(c.UserContext(), "eo_key_required")
	}
	if f.Label == "" {
		errs["label"] = i18n.T(c.UserContext(), "eo_label_required")
	}
	// The kind is checked against models.EOFieldKinds, which is built from the
	// 27 EOKind* constants themselves rather than being a second list of
	// strings here that could drift from them. Before this, the 27 constants
	// were referenced only by the seed and the designer accepted any non-empty
	// string: a typo like "drop_down" stored a field whose kind nothing can
	// ever render or validate.
	//
	// This validates the NAME of the kind. Per-kind validation of a stored
	// VALUE at save time is Plan 2 work and is deliberately not attempted here.
	switch {
	case f.Kind == "":
		errs["kind"] = i18n.T(c.UserContext(), "eo_kind_required")
	case !models.IsEOFieldKind(f.Kind):
		errs["kind"] = i18n.Tf(c.UserContext(), "eo_kind_invalid", strings.Join(models.EOFieldKinds, ", "))
	}
	switch f.Provenance {
	case "", models.EOProvenanceUser, models.EOProvenanceAuto, models.EOProvenanceDerived, models.EOProvenanceSystem:
	default:
		errs["provenance"] = i18n.T(c.UserContext(), "eo_provenance_invalid")
	}
	switch f.RequiredMode {
	case "", models.EORequiredNever, models.EORequiredAlways, models.EORequiredConditional, models.EORequiredAtState:
	default:
		errs["required_mode"] = i18n.T(c.UserContext(), "eo_required_mode_invalid")
	}
	// A derived field without a formula computes nothing and would look
	// configured; a formula on a field nothing recomputes never runs.
	if f.Provenance == models.EOProvenanceDerived && strings.TrimSpace(f.Formula) == "" {
		errs["formula"] = i18n.T(c.UserContext(), "eo_formula_required")
	}
	return errs
}

// ── link types ──────────────────────────────────────────────────────────────

// eoLinkTypeRequest is the editable attribute set of a link type (spec §7.6).
type eoLinkTypeRequest struct {
	Key                       string         `json:"key"`
	Label                     string         `json:"label"`
	LabelAr                   string         `json:"label_ar"`
	SourceTypeIDs             datatypes.JSON `json:"source_type_ids"`
	TargetTypeIDs             datatypes.JSON `json:"target_type_ids"`
	Cardinality               string         `json:"cardinality"`
	DeriveDeadlineFromParent  bool           `json:"derive_deadline_from_parent"`
	DeadlineOffsetDays        int            `json:"deadline_offset_days"`
	BlocksParentClosure       bool           `json:"blocks_parent_closure"`
	BlocksParentTransitionKey string         `json:"blocks_parent_transition_key"`
	CascadeSensitivity        bool           `json:"cascade_sensitivity"`
}

func (r *eoLinkTypeRequest) applyTo(lt *models.EOLinkType) {
	lt.Key = strings.TrimSpace(r.Key)
	lt.Label = strings.TrimSpace(r.Label)
	lt.LabelAr = strings.TrimSpace(r.LabelAr)
	if len(r.SourceTypeIDs) > 0 {
		lt.SourceTypeIDs = r.SourceTypeIDs
	}
	if len(r.TargetTypeIDs) > 0 {
		lt.TargetTypeIDs = r.TargetTypeIDs
	}
	lt.Cardinality = strings.TrimSpace(r.Cardinality)
	lt.DeriveDeadlineFromParent = r.DeriveDeadlineFromParent
	lt.DeadlineOffsetDays = r.DeadlineOffsetDays
	lt.BlocksParentClosure = r.BlocksParentClosure
	lt.BlocksParentTransitionKey = strings.TrimSpace(r.BlocksParentTransitionKey)
	lt.CascadeSensitivity = r.CascadeSensitivity
}

func (h *EORecordTypeHandler) ListLinkTypes(c *fiber.Ctx) error {
	out, err := h.types.ListLinkTypes(c.UserContext())
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if out == nil {
		out = []models.EOLinkType{}
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_link_types_retrieved"), out)
}

func (h *EORecordTypeHandler) CreateLinkType(c *fiber.Ctx) error {
	var req eoLinkTypeRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	lt := &models.EOLinkType{ID: uuid.New()}
	req.applyTo(lt)
	live, err := h.eoLiveTypes(c)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if fieldErrs := eoValidateLinkTypeShape(c, lt, live); len(fieldErrs) > 0 {
		return eoFieldErrors(c, fieldErrs)
	}
	existing, err := h.types.GetLinkTypeByKey(c.UserContext(), lt.Key)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if existing != nil {
		return eoFieldErrors(c, map[string]string{"key": i18n.T(c.UserContext(), "eo_link_type_key_exists")})
	}
	if err := h.types.CreateLinkType(c.UserContext(), lt); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	return utils.SuccessResponse(c, fiber.StatusCreated, i18n.T(c.UserContext(), "eo_link_type_created"), lt)
}

func (h *EORecordTypeHandler) UpdateLinkType(c *fiber.Ctx) error {
	id, ok, err := eoParamUUID(c, "link_type_id")
	if !ok {
		return err
	}
	var req eoLinkTypeRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	stored, err := h.types.GetLinkTypeByID(c.UserContext(), id)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if stored == nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "eo_link_type_not_found"))
	}
	updated := *stored
	req.applyTo(&updated)
	live, err := h.eoLiveTypes(c)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if fieldErrs := eoValidateLinkTypeShape(c, &updated, live); len(fieldErrs) > 0 {
		return eoFieldErrors(c, fieldErrs)
	}
	if err := h.types.UpdateLinkType(c.UserContext(), &updated); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_link_type_updated"), &updated)
}

func (h *EORecordTypeHandler) DeleteLinkType(c *fiber.Ctx) error {
	id, ok, err := eoParamUUID(c, "link_type_id")
	if !ok {
		return err
	}
	stored, err := h.types.GetLinkTypeByID(c.UserContext(), id)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if stored == nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "eo_link_type_not_found"))
	}
	if err := h.types.DeleteLinkType(c.UserContext(), id); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_link_type_deleted"), nil)
}

// eoLiveTypes indexes every live (not soft-deleted) record type by id.
func (h *EORecordTypeHandler) eoLiveTypes(c *fiber.Ctx) (map[uuid.UUID]models.EORecordType, error) {
	all, err := h.types.List(c.UserContext())
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]models.EORecordType, len(all))
	for _, t := range all {
		out[t.ID] = t
	}
	return out, nil
}

// eoValidateLinkEndpoints checks one endpoint list against the live types.
// Spellings accepted are the ones services.eoTypeAllowed honours: the bare
// JSON string "any", or an array of strings each of which is "any" or the id
// of a live CONCRETE type. Everything else is refused here, in front of the
// administrator, rather than at record-link time in front of an officer.
//
// THE RAW VALUE IS DECODED BEFORE ANY CHECK AND NEVER NORMALISED AWAY: the
// empty test reads the decoded list, so "[]", "null" and a missing list all
// land on the same refusal.
func eoValidateLinkEndpoints(c *fiber.Ctx, raw []byte, live map[uuid.UUID]models.EORecordType) string {
	ctx := c.UserContext()
	if t := strings.TrimSpace(string(raw)); t == "" || t == "null" {
		return i18n.T(ctx, "eo_link_endpoints_required")
	}
	var ids []string
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		ids = []string{single}
	} else if err := json.Unmarshal(raw, &ids); err != nil {
		return i18n.T(ctx, "eo_link_endpoints_malformed")
	}
	if len(ids) == 0 {
		return i18n.T(ctx, "eo_link_endpoints_required")
	}
	for _, s := range ids {
		if s == "any" {
			continue
		}
		id, err := uuid.Parse(s)
		t, found := live[id]
		if err != nil || !found {
			return fmt.Sprintf("%s: %s", i18n.T(ctx, "eo_link_endpoint_unknown"), s)
		}
		if t.IsAbstract {
			return fmt.Sprintf("%s: %s", i18n.T(ctx, "eo_link_endpoint_abstract"), t.Key)
		}
	}
	return ""
}

func eoValidateLinkTypeShape(c *fiber.Ctx, lt *models.EOLinkType, live map[uuid.UUID]models.EORecordType) map[string]string {
	errs := map[string]string{}
	if lt.Key == "" {
		errs["key"] = i18n.T(c.UserContext(), "eo_key_required")
	}
	if lt.Label == "" {
		errs["label"] = i18n.T(c.UserContext(), "eo_label_required")
	}
	switch lt.Cardinality {
	case "", models.EOCardinalityOne, models.EOCardinalityMany:
	default:
		errs["cardinality"] = i18n.T(c.UserContext(), "eo_cardinality_invalid")
	}
	if msg := eoValidateLinkEndpoints(c, lt.SourceTypeIDs, live); msg != "" {
		errs["source_type_ids"] = msg
	}
	if msg := eoValidateLinkEndpoints(c, lt.TargetTypeIDs, live); msg != "" {
		errs["target_type_ids"] = msg
	}
	return errs
}
