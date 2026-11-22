package handlers

import (
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/i18n"
	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// EORecordTypeHandler is the designer: record types, their field definitions
// and the link types between them (spec §7.2, §7.3, §7.6). Everything it
// exposes sits behind eo:types:manage.
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
	const manage = "eo:types:manage"

	r.Get("/link-types", perm(manage), h.ListLinkTypes)
	r.Post("/link-types", perm(manage), h.CreateLinkType)
	r.Put("/link-types/:link_type_id", perm(manage), h.UpdateLinkType)
	r.Delete("/link-types/:link_type_id", perm(manage), h.DeleteLinkType)

	r.Get("/", perm(manage), h.List)
	r.Post("/", perm(manage), h.Create)
	r.Get("/:id", perm(manage), h.Get)
	r.Put("/:id", perm(manage), h.Update)
	r.Delete("/:id", perm(manage), h.Delete)

	r.Get("/:id/fields", perm(manage), h.ListFields)
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
//	if allowed, err := h.eoGuardTypeEdit(...); !allowed {
//		return err
//	}
//
// and can no longer reach its repository write. The bool is load-bearing:
// utils.ErrorResponse returns nil on a successful write, so a refusal handed
// back in the error slot alone would read as "edit allowed" at every call
// site and the destructive write would go through under a 400 response.
func (h *EORecordTypeHandler) eoGuardTypeEdit(
	c *fiber.Ctx,
	before, after models.EORecordType,
	fieldsBefore, fieldsAfter []models.EOFieldDef,
) (bool, error) {
	hasRecords, err := h.eoTypeHasRecords(c, before.ID)
	if err != nil {
		return false, utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	if err := models.ValidateTypeEdit(before, after, fieldsBefore, fieldsAfter, hasRecords); err != nil {
		// The message names the field and the reason; it is the whole point
		// of the guard rail and is returned verbatim rather than flattened
		// into a generic "invalid".
		return false, utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	return true, nil
}

// eoTypeHasRecords asks the register, under an admin scope: the question is
// "does this type hold anything", not "does the caller see anything", and a
// visibility-scoped count would let a restricted record be destroyed by an
// administrator who cannot see it.
func (h *EORecordTypeHandler) eoTypeHasRecords(c *fiber.Ctx, typeID uuid.UUID) (bool, error) {
	_, total, err := h.records.List(c.UserContext(), &models.EORecordFilter{
		RecordTypeID: []string{typeID.String()},
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
// An abstract type holds no records of its own and so needs no prefix.
func (h *EORecordTypeHandler) eoCheckReferencePrefix(c *fiber.Ctx, t *models.EORecordType) (bool, error) {
	if t.IsAbstract {
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
	if err := models.ValidateReferencePrefix(t.ReferencePrefix, others); err != nil {
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
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
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
	// both sides: what §7.14 judges here is the reference prefix and pattern.
	if allowed, err := h.eoGuardTypeEdit(c, before, after, fields, fields); !allowed {
		return err
	}
	if allowed, err := h.eoCheckReferencePrefix(c, &after); !allowed {
		return err
	}
	if err := h.types.Update(c.UserContext(), &after); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}
	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "eo_record_type_updated"), &after)
}

// Delete soft-deletes a record type, and REFUSES while it holds records. A
// type is the partition key of the register: deleting one whose records exist
// would orphan a statutory audit trail behind a configuration row nobody can
// read any more. Removing those records is not this endpoint's to decide.
func (h *EORecordTypeHandler) Delete(c *fiber.Ctx) error {
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	if _, loaded, loadErr := h.eoLoadType(c, id); !loaded {
		return loadErr
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
	if allowed, err := h.eoGuardTypeEdit(c, *t, *t, before, after); !allowed {
		return err
	}
	if err := h.types.CreateFieldDef(c.UserContext(), def); err != nil {
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
	if allowed, err := h.eoGuardTypeEdit(c, *t, *t, before, after); !allowed {
		return err
	}
	if err := h.types.UpdateFieldDef(c.UserContext(), &updated); err != nil {
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
	if allowed, err := h.eoGuardTypeEdit(c, *t, *t, before, after); !allowed {
		return err
	}
	if err := h.types.DeleteFieldDef(c.UserContext(), fieldID); err != nil {
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
	if f.Kind == "" {
		errs["kind"] = i18n.T(c.UserContext(), "eo_kind_required")
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
	if fieldErrs := eoValidateLinkTypeShape(c, lt); len(fieldErrs) > 0 {
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
	if fieldErrs := eoValidateLinkTypeShape(c, &updated); len(fieldErrs) > 0 {
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

func eoValidateLinkTypeShape(c *fiber.Ctx, lt *models.EOLinkType) map[string]string {
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
	return errs
}
