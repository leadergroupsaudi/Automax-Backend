package handlers

import (
	"errors"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/i18n"
	"github.com/automax/backend/pkg/utils"
	"github.com/automax/backend/pkg/validation"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// eoDefaultPageLimit and eoMaxPageLimit mirror the repository's own clamp
// (repository/eo_record_repository.go). The handler clamps a SECOND time
// because utils.PaginatedSuccessResponse divides the total by the limit it is
// handed, unguarded (pkg/utils/response.go:165): ?limit=0 reaching it is a
// division by zero, which is a panic, not a 400.
const (
	eoDefaultPageLimit = 20
	eoMaxPageLimit     = 100
)

// EOPermissionMiddleware builds the permission guard for one or more
// permission codes — in the server it is authMiddleware.RequirePermission.
// RegisterRoutes takes it as a parameter so the route SET and its ORDER live
// in one place that a test can register on a bare app, instead of being
// spelled out a second time in main.go where nothing checks it.
type EOPermissionMiddleware func(permissions ...string) fiber.Handler

// EORecordHandler is the HTTP surface of the EO record engine.
//
// It holds the two services AND the two repositories: the register query
// (List), the ancestry read (Children) and the typed link write have no
// service of their own in Plan 1 — the save pipeline and the transition
// executor are the services, and everything else is a read the repository
// already answers.
type EORecordHandler struct {
	records     repository.EORecordRepository
	types       repository.EORecordTypeRepository
	recordSvc   services.EORecordService
	transitions services.EOTransitionService
}

func NewEORecordHandler(
	recordSvc services.EORecordService,
	transitions services.EOTransitionService,
	records repository.EORecordRepository,
	types repository.EORecordTypeRepository,
) *EORecordHandler {
	return &EORecordHandler{
		recordSvc:   recordSvc,
		transitions: transitions,
		records:     records,
		types:       types,
	}
}

// RegisterRoutes registers the record routes on r.
//
// Fiber matches in REGISTRATION order, so every literal sub-path is registered
// before /:id. Registering GET /:id first would make /eo-records/my-assigned
// resolve to the by-id handler, which would answer 400 for an unparseable id.
//
// There is deliberately no DELETE: repository.EORecordRepository has no Delete
// and the EO register is a statutory audit trail — what deletion means there
// (soft or hard, across the partitions, with eo_record_links pointing both
// ways, with an issued reference that must never be reused) is a design
// question this plan does not settle. The same goes for a stats aggregate:
// nothing computes one, and a count query invented at the HTTP boundary would
// not respect §7.11 row-level visibility the way List does.
func (h *EORecordHandler) RegisterRoutes(r fiber.Router, perm EOPermissionMiddleware) {
	r.Post("/", perm("eo:create"), h.Create)
	r.Get("/", perm("eo:view"), h.List)
	r.Get("/my-assigned", perm("eo:view"), h.MyAssigned)
	// Search is the only list route that binds a BODY. It is a literal path
	// and registers before /:id for the same reason my-assigned does.
	r.Post("/search", perm("eo:view"), h.Search)
	r.Get("/:id", perm("eo:view"), h.Get)
	r.Put("/:id", perm("eo:update"), h.Update)
	r.Get("/:id/transitions", perm("eo:view"), h.AvailableTransitions)
	r.Post("/:id/transition", perm("eo:transition"), h.Transition)
	r.Get("/:id/children", perm("eo:view"), h.Children)
	r.Post("/:id/links", perm("eo:update"), h.CreateLink)
}

// ── shared plumbing ─────────────────────────────────────────────────────────

// The helpers below may ANSWER the request themselves. Each returns
// (value, ok, err): ok is false when it has written the response, and err is
// what that write returned — nil when it succeeded. The caller writes
//
//	v, ok, err := helper(c)
//	if !ok {
//		return err
//	}
//
// and never returns a non-nil marker to fiber, which would make the global
// error handler write a second response over the first.
//
// The bool is not decoration. utils.ErrorResponse returns NIL when the write
// succeeds, so a helper that handed its refusal back in an error slot alone
// would read as "no problem" at every call site and each handler would carry
// straight on past its own 401 — with, in eoActor's case, a nil actor.

// eoActor reads the authenticated user in the checked form. The engine records
// a creator on every record and resolves "who may run this transition" from
// the actor, so there is no anonymous path through any of these handlers.
func eoActor(c *fiber.Ctx) (*models.User, bool, error) {
	user, ok := c.Locals(constants.ContextKeys.User).(*models.User)
	if !ok || user == nil {
		return nil, false, utils.ErrorResponse(c, fiber.StatusUnauthorized, i18n.T(c.UserContext(), "eo_actor_required"))
	}
	return user, true, nil
}

// eoFieldErrors is the {"success":false,"errors":{field:message}} shape the EO
// forms read, which is a MAP — not the {"error":"..."} string shape.
func eoFieldErrors(c *fiber.Ctx, fields map[string]string) error {
	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
		"success": false,
		"errors":  fields,
	})
}

// eoServiceError maps a service refusal to its status explicitly. A validation
// failure is never a 500, and a configuration failure is never a 400: a record
// type naming an unregistered capability (services.ErrEOUnknownCapability)
// comes back here, is logged by InternalErrorResponse and is answered 500 —
// it is NOT swallowed into an empty list or a success.
func eoServiceError(c *fiber.Ctx, err error) error {
	var invalid *services.EOValidationError
	switch {
	case errors.As(err, &invalid):
		return eoFieldErrors(c, invalid.Fields)
	case errors.Is(err, services.ErrEORecordNotFound):
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "eo_record_not_found"))
	case errors.Is(err, services.ErrEOTransitionNotAvailable):
		return utils.ErrorResponse(c, fiber.StatusConflict, i18n.T(c.UserContext(), "eo_transition_not_available"))
	case errors.Is(err, services.ErrEOTransitionBlocked):
		return utils.ErrorResponse(c, fiber.StatusConflict, i18n.T(c.UserContext(), "eo_transition_blocked"))
	case errors.Is(err, services.ErrEOTransitionNotPermitted):
		return utils.ErrorResponse(c, fiber.StatusForbidden, i18n.T(c.UserContext(), "eo_transition_not_permitted"))
	case errors.Is(err, services.ErrEOUnknownCapability), errors.Is(err, services.ErrEOCapabilityConfig):
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "eo_configuration_invalid"))
	default:
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
}

// eoScopeFilter fills the fields the client must never set: §7.11 row-level
// visibility is decided from the session, not the query string. EORecordFilter
// tags them query:"-" so ?IsAdmin=true cannot reach them, and they are
// assigned here unconditionally so an absent tag could not open them either.
func eoScopeFilter(filter *models.EORecordFilter, user *models.User) {
	filter.UserID = user.ID
	filter.IsAdmin = user.IsSuperAdmin
	filter.VisibleToDepartmentIDs = eoUserBodies(user)
}

// eoUserBodies is every body the user belongs to: the primary department and
// the many-to-many memberships, de-duplicated.
func eoUserBodies(user *models.User) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := []uuid.UUID{}
	add := func(id uuid.UUID) {
		if id == uuid.Nil || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	if user.DepartmentID != nil {
		add(*user.DepartmentID)
	}
	for _, d := range user.Departments {
		add(d.ID)
	}
	return out
}

// eoClampPaging resolves page and limit the way the repository does, and
// returns the values the paginated envelope must be built from. A limit of
// zero never leaves this function.
func eoClampPaging(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > eoMaxPageLimit {
		limit = eoDefaultPageLimit
	}
	return page, limit
}

// eoParamUUID reads a uuid path parameter, answering 400 rather than letting
// uuid.Nil reach a query.
func eoParamUUID(c *fiber.Ctx, name string) (uuid.UUID, bool, error) {
	id, err := uuid.Parse(c.Params(name))
	if err != nil {
		return uuid.Nil, false, utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_id"))
	}
	return id, true, nil
}

// eoLoadRecord reads a record by id alone and applies §7.11 visibility to the
// single-record read: GetAnyByID crosses every partition and knows nothing
// about who is asking, so a record restricted to a body it does not belong to
// must come back as "not found" here — exactly as List would have hidden it.
// Absence and invisibility deliberately give the same answer, so the endpoint
// cannot be used to probe for references.
func (h *EORecordHandler) eoLoadRecord(c *fiber.Ctx, id uuid.UUID, user *models.User) (*models.EORecord, bool, error) {
	rec, err := h.records.GetAnyByID(c.UserContext(), id)
	if err != nil {
		return nil, false, utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if rec == nil {
		return nil, false, utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "eo_record_not_found"))
	}
	visible, err := h.eoVisible(c, rec, user)
	if err != nil {
		// A failed visibility probe is an OUTAGE, not an answer. Reporting it
		// as 404 would be safe in direction and badly wrong in meaning: it
		// tells an operator a record was deleted, logs nothing, and makes a
		// database incident look like a data incident.
		return nil, false, utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if !visible {
		return nil, false, utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "eo_record_not_found"))
	}
	return rec, true, nil
}

// eoVisible re-asks the register for this one record under the caller's scope.
// The visibility rule lives in the repository's WHERE clause and exists in one
// place (applyEORecordVisibility); re-implementing it here against the values
// bag would be a second copy that drifts.
//
// It returns the repository's error rather than folding it into false: "the
// caller may not see this" and "the database did not answer" are different
// facts and must not share a status code.
func (h *EORecordHandler) eoVisible(c *fiber.Ctx, rec *models.EORecord, user *models.User) (bool, error) {
	filter := &models.EORecordFilter{
		RecordID:     &rec.ID,
		RecordTypeID: []string{rec.RecordTypeID.String()},
		Limit:        1,
	}
	eoScopeFilter(filter, user)
	if filter.IsAdmin {
		return true, nil
	}
	rows, _, err := h.records.List(c.UserContext(), filter)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// ── handlers ────────────────────────────────────────────────────────────────

func (h *EORecordHandler) Create(c *fiber.Ctx) error {
	user, ok, err := eoActor(c)
	if !ok {
		return err
	}
	var req models.EORecordCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	if req.RecordTypeID == uuid.Nil {
		return eoFieldErrors(c, map[string]string{"record_type_id": i18n.T(c.UserContext(), "eo_record_type_required")})
	}
	// A parent_record_id is a READ of that record, and §7.11 applies to it.
	// The service loads the parent with GetAnyByID, which crosses every
	// partition and knows nothing about who is asking — so without this a
	// caller holding eo:create who merely KNOWS an id could parent a record
	// under one that GET /eo-records/:id correctly answers 404 for. Three
	// things follow from that: hierarchical numbering renders the parent's
	// reference INSIDE the child's, disclosing a reference the caller may not
	// read; the child appears in the restricted record's Children; and if the
	// link type sets blocks_parent_closure, an outsider's child blocks another
	// body's record from closing.
	//
	// CreateLink — the sibling write — already resolves both of its endpoints
	// through eoLoadRecord. This is the same check, and deliberately returns
	// the same 404: absence and invisibility stay indistinguishable, as
	// eoLoadRecord's own doc comment requires.
	if req.ParentRecordID != nil {
		if _, visible, loadErr := h.eoLoadRecord(c, *req.ParentRecordID, user); !visible {
			return loadErr
		}
	}
	rec, err := h.recordSvc.Create(c.UserContext(), &req, user)
	if err != nil {
		return eoServiceError(c, err)
	}
	return utils.SuccessResponse(c, fiber.StatusCreated, i18n.T(c.UserContext(), "eo_record_created"), rec)
}

func (h *EORecordHandler) List(c *fiber.Ctx) error {
	user, ok, err := eoActor(c)
	if !ok {
		return err
	}
	var filter models.EORecordFilter
	if err := c.QueryParser(&filter); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	return h.list(c, &filter, user)
}

// Search is List with the filter in the REQUEST BODY instead of the query
// string.
//
// It exists because five of EORecordFilter's fields — Values, CreatedFrom,
// CreatedTo, DueFrom, DueTo — are tagged query:"-" and documented "request
// body only", and until this route no EO endpoint bound a body into an
// EORecordFilter at all: List, MyAssigned and Children all use QueryParser. So
// the repository's jsonb containment predicate (eo_records."values" @> ?::jsonb)
// and its four date predicates could never run, and idx_eo_records_values_gin
// existed to serve a filter nothing could express.
//
// A body, not a longer query string, because a jsonb containment filter is a
// nested object: ?values[a][b]=c has no agreed encoding, and a URL has a
// length limit a saved register view would eventually meet.
//
// It goes through the SAME h.list as the other list routes, so scope filling,
// the limit clamp, sort validation and the response envelope stay in one
// place — including eoScopeFilter, which overwrites the three server-filled
// fields AFTER binding, exactly as it does everywhere else.
func (h *EORecordHandler) Search(c *fiber.Ctx) error {
	user, ok, err := eoActor(c)
	if !ok {
		return err
	}
	var filter models.EORecordFilter
	if err := c.BodyParser(&filter); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	return h.list(c, &filter, user)
}

// MyAssigned is List with the actor's id in the assignee filter. It is a
// literal path, so it is registered before /:id.
func (h *EORecordHandler) MyAssigned(c *fiber.Ctx) error {
	user, ok, err := eoActor(c)
	if !ok {
		return err
	}
	var filter models.EORecordFilter
	if err := c.QueryParser(&filter); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	// Assigned to ME, whatever the query string said: the route is the
	// statement, not a default the caller may widen.
	filter.AssignedUser = []string{user.ID.String()}
	return h.list(c, &filter, user)
}

func (h *EORecordHandler) list(c *fiber.Ctx, filter *models.EORecordFilter, user *models.User) error {
	if fieldErrs := validation.ValidateStruct(c.UserContext(), filter); len(fieldErrs) > 0 {
		return eoFieldErrors(c, fieldErrs)
	}
	page, limit := eoClampPaging(filter.Page, filter.Limit)
	filter.Page, filter.Limit = page, limit
	eoScopeFilter(filter, user)

	rows, total, err := h.records.List(c.UserContext(), filter)
	if err != nil {
		// An unknown sort_by or sort_dir is the caller's mistake, not the
		// server's: the repository raises it from its allow-list before any
		// query runs. It is matched on the sentinel, because duplicating
		// eoSortColumns here would be a second copy to keep in step. Without
		// this branch, ?sort_by=bogus answers 500 and writes an "internal
		// error" line to the log for somebody's typo.
		if errors.Is(err, repository.ErrEOUnsupportedSort) {
			return eoFieldErrors(c, map[string]string{
				"sort_by": i18n.Tf(c.UserContext(), "eo_sort_by_unsupported", strings.Join(repository.EOSortKeys(), ", ")),
			})
		}
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if rows == nil {
		rows = []models.EORecord{}
	}
	return utils.PaginatedSuccessResponse(c, rows, page, limit, total)
}

func (h *EORecordHandler) Get(c *fiber.Ctx) error {
	user, ok, err := eoActor(c)
	if !ok {
		return err
	}
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	rec, ok, err := h.eoLoadRecord(c, id, user)
	if !ok {
		return err
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_record_retrieved"), rec)
}

func (h *EORecordHandler) Update(c *fiber.Ctx) error {
	user, ok, err := eoActor(c)
	if !ok {
		return err
	}
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	var req models.EORecordUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	// The record is loaded under the CALLER'S SCOPE unconditionally, before
	// anything else. It is not enough to do this only when the body omits
	// record_type_id: that made the visibility gate optional, because the
	// caller chooses whether to supply the field that skips it. Anyone who
	// knew (or guessed) an id and its type could then update a record that
	// GET /eo-records/:id would correctly answer 404 for.
	stored, loaded, loadErr := h.eoLoadRecord(c, id, user)
	if !loaded {
		return loadErr
	}
	// A supplied type that does not match the stored one gets the SAME answer
	// as a record that is not there: a 400 saying "wrong record_type_id"
	// would confirm the record exists and that the guess was close, which is
	// exactly the probing oracle eoLoadRecord's 404 avoids.
	if req.RecordTypeID != uuid.Nil && req.RecordTypeID != stored.RecordTypeID {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "eo_record_not_found"))
	}
	// The primary key is composite; the service reads the record with it. It
	// comes from the stored record, never from the request.
	req.RecordTypeID = stored.RecordTypeID

	rec, err := h.recordSvc.Update(c.UserContext(), id, &req, user)
	if err != nil {
		return eoServiceError(c, err)
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_record_updated"), rec)
}

func (h *EORecordHandler) AvailableTransitions(c *fiber.Ctx) error {
	user, ok, err := eoActor(c)
	if !ok {
		return err
	}
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	rec, ok, err := h.eoLoadRecord(c, id, user)
	if !ok {
		return err
	}
	out, err := h.transitions.AvailableTransitions(c.UserContext(), rec, user)
	if err != nil {
		return eoServiceError(c, err)
	}
	if out == nil {
		out = []models.WorkflowTransition{}
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_transitions_retrieved"), out)
}

// eoTransitionRequest is the transition modal's payload: which transition, and
// the comment, attachments and field values its requirements ask for.
type eoTransitionRequest struct {
	TransitionID uuid.UUID `json:"transition_id"`
	models.EOTransitionInput
}

func (h *EORecordHandler) Transition(c *fiber.Ctx) error {
	user, ok, err := eoActor(c)
	if !ok {
		return err
	}
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	var req eoTransitionRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	if req.TransitionID == uuid.Nil {
		return eoFieldErrors(c, map[string]string{"transition_id": i18n.T(c.UserContext(), "eo_transition_required")})
	}
	rec, ok, err := h.eoLoadRecord(c, id, user)
	if !ok {
		return err
	}
	out, err := h.transitions.Execute(c.UserContext(), rec, req.TransitionID, user, req.EOTransitionInput)
	if err != nil {
		return eoServiceError(c, err)
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_transition_executed"), out)
}

// Children lists the direct children of the record in the path.
//
// It goes through List with a parent_record_id filter, NOT through
// repository.EORecordRepository.Children. Children takes no actor and has no
// visibility clause, so serving it raw would mean that seeing a parent yields
// every record beneath it — including the ones §7.11 restricts to a body the
// caller does not belong to. In a register where sensitivity CASCADES down
// the tree, the children are precisely the rows the rule is protecting.
//
// Both queries select on parent_record_id, so this is the same set of records,
// minus the ones the caller may not see. It is paginated, because List is.
func (h *EORecordHandler) Children(c *fiber.Ctx) error {
	user, ok, err := eoActor(c)
	if !ok {
		return err
	}
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	// The parent itself must be visible: the children of a record the caller
	// may not see are not the caller's to enumerate either.
	if _, loaded, loadErr := h.eoLoadRecord(c, id, user); !loaded {
		return loadErr
	}
	var filter models.EORecordFilter
	if err := c.QueryParser(&filter); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	// The parent is the route, not a default the caller may widen.
	parentID := id.String()
	filter.ParentID = &parentID
	// h.list validates, clamps page and limit, applies the caller's scope and
	// answers with the paginated envelope — the one place all of that lives.
	return h.list(c, &filter, user)
}

// eoLinkRequest asks for one typed link from the record in the path to another
// record.
type eoLinkRequest struct {
	ToRecordID  uuid.UUID `json:"to_record_id"`
	LinkTypeKey string    `json:"link_type_key"`
}

// CreateLink writes a typed link between two existing records (spec §7.6).
//
// It does NOT apply the link type's deadline derivation or sensitivity
// cascade: those are rules of a record's BIRTH under a parent, applied by the
// save pipeline inside the create transaction, and re-applying them to an
// already-numbered record would silently rewrite its due date long after the
// SLA clock on it started. A link added here is a relationship, not a
// re-parenting. What it does enforce is services.ValidateLink — the endpoints'
// types, the cardinality and the duplicate check.
func (h *EORecordHandler) CreateLink(c *fiber.Ctx) error {
	user, ok, err := eoActor(c)
	if !ok {
		return err
	}
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	var req eoLinkRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}
	fieldErrs := map[string]string{}
	if req.ToRecordID == uuid.Nil {
		fieldErrs["to_record_id"] = i18n.T(c.UserContext(), "invalid_id")
	}
	if req.LinkTypeKey == "" {
		fieldErrs["link_type_key"] = i18n.T(c.UserContext(), "eo_link_type_required")
	}
	if len(fieldErrs) > 0 {
		return eoFieldErrors(c, fieldErrs)
	}

	from, ok, err := h.eoLoadRecord(c, id, user)
	if !ok {
		return err
	}
	to, ok, err := h.eoLoadRecord(c, req.ToRecordID, user)
	if !ok {
		return err
	}
	lt, err := h.types.GetLinkTypeByKey(c.UserContext(), req.LinkTypeKey)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if lt == nil {
		return eoFieldErrors(c, map[string]string{"link_type_key": i18n.T(c.UserContext(), "eo_link_type_unknown")})
	}
	existing, err := h.records.LinksFrom(c.UserContext(), from.ID)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if err := services.ValidateLink(lt, from, to, existing); err != nil {
		// ValidateLink's refusals are configuration-level statements about
		// this pair ("already linked", "cardinality one"), so they are
		// returned verbatim against the field that caused them.
		return eoFieldErrors(c, map[string]string{"link_type_key": err.Error()})
	}
	link := &models.EORecordLink{
		ID:               uuid.New(),
		FromRecordID:     from.ID,
		FromRecordTypeID: from.RecordTypeID,
		ToRecordID:       to.ID,
		ToRecordTypeID:   to.RecordTypeID,
		LinkTypeKey:      lt.Key,
		CreatedByID:      &user.ID,
	}
	if err := h.records.CreateLink(c.UserContext(), link); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	return utils.SuccessResponse(c, fiber.StatusCreated, i18n.T(c.UserContext(), "eo_link_created"), link)
}
