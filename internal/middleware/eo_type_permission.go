package middleware

import (
	"log"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/i18n"
	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// EOTypePermission is spec §7.11's third visibility mechanism: PER-TYPE
// permissions. The other two — the sensitivity label and row-level visible_to
// — shipped in Plan 1; the codes eo:<type_key>:view|create|update were seeded
// and granted in the same plan and, until this file existed, nothing consulted
// them. An administrator ticking one of those boxes was, in the spec's own
// words, "expressing an intention, not applying a control".
//
// IT IS HALF OF THE CONTROL. This gate answers 403 for ONE record, reached by
// its id or named by a create body. The register's LIST is filtered
// separately, in repository.applyEORecordTypePermissions, because a route
// check cannot narrow a result set: without the repository clause a caller
// refused eo:eo_decision_record:view here would still be shown every Decision
// Record by GET /eo-records. Both surfaces build their code with
// models.EOTypePermissionCode, so neither can drift from the seed.
//
// INHERITANCE IS NOT WALKED. A record belongs to exactly one CONCRETE type and
// the seed writes a code for each concrete type; holding an abstract parent's
// code grants nothing. That is deliberate — the abstract types hold no records
// of their own, so a parent code would be a grant on an empty set that
// silently widened the moment a child type was added.
type EOTypePermission struct {
	records repository.EORecordRepository
	types   repository.EORecordTypeRepository
}

func NewEOTypePermission(records repository.EORecordRepository, types repository.EORecordTypeRepository) *EOTypePermission {
	return &EOTypePermission{records: records, types: types}
}

// eoTypePermissionBody is the one field the gate reads from a create body. It
// carries the same tags Fiber's BodyParser matches on, and the body is
// buffered: the handler parses the same bytes again afterwards.
type eoTypePermissionBody struct {
	RecordTypeID string `json:"record_type_id" form:"record_type_id" xml:"record_type_id"`
}

// RequireEOTypePermission refuses a caller who does not hold
// eo:<type_key>:<action> for the record type this request names.
//
// IT RUNS AFTER the module-level guard, never instead of it:
// AuthMiddleware.RequirePermission is what loads the user WITH their
// permissions and leaves them in Locals, and eo:view / eo:create / eo:update /
// eo:transition still gate the module as a whole. This narrows that grant to
// the types the caller was actually given. If the user is NOT in Locals — the
// gate registered first, or registered on a route with no module-level guard
// at all — it refuses rather than passing: a check that cannot see who is
// calling has not made a decision.
//
// The type is resolved from the record id in the path, or, on a route that
// carries no id, from the body's record_type_id. A request that names no type
// at all is handed on: the handler answers 400 for a missing record_type_id
// and 400 for an unparseable id, and a gate that answered those itself would
// change what the endpoint returns for a malformed request without ever
// refusing anybody.
func (m *EOTypePermission) RequireEOTypePermission(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		user, ok := c.Locals(constants.ContextKeys.User).(*models.User)
		if !ok || user == nil {
			// 403 and not 401: the frontend's axios interceptor logs the user
			// out on a 401, and this is a refusal, not a missing session.
			log.Printf("[EO] per-type gate reached with no authenticated user on %s %s", c.Method(), c.Path())
			return utils.ErrorResponse(c, fiber.StatusForbidden, "Insufficient permissions")
		}
		if user.IsSuperAdmin {
			return c.Next()
		}

		typeID, decided, err := m.resolveTypeID(c)
		if decided {
			return err
		}
		if typeID == uuid.Nil {
			// No type is named. The handler owns that answer.
			return c.Next()
		}

		recordType, err := m.types.GetByID(c.UserContext(), typeID)
		if err != nil {
			return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
		}
		if recordType == nil {
			// A type that is not in the catalogue has no code to hold, so
			// nobody holds it. Refusing is the only safe reading.
			return utils.ErrorResponse(c, fiber.StatusForbidden, "Insufficient permissions")
		}

		code := models.EOTypePermissionCode(recordType.Key, action)
		if !user.HasPermission(code) {
			log.Printf("[EO] per-type permission denied for user %s: required %s", user.Email, code)
			return utils.ErrorResponse(c, fiber.StatusForbidden, "Insufficient permissions")
		}
		return c.Next()
	}
}

// resolveTypeID answers which record type this request is about.
//
// It returns (id, decided, err). decided is true when it has ANSWERED the
// request — a record that is not there, or a register that did not answer —
// and the caller must return err unchanged. uuid.Nil with decided false means
// "this request names no type", which is the handler's business.
func (m *EOTypePermission) resolveTypeID(c *fiber.Ctx) (uuid.UUID, bool, error) {
	if raw := c.Params("id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			// The handler answers 400 invalid_id for this.
			return uuid.Nil, false, nil
		}
		rec, err := m.records.GetAnyByID(c.UserContext(), id)
		if err != nil {
			// An outage is not an answer about permissions, and it is not a
			// 404 either: that would tell an operator a record was deleted.
			return uuid.Nil, true, utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
		}
		if rec == nil {
			// Exactly what the handler would have answered, so the gate does
			// not make a missing record look different from an invisible one.
			return uuid.Nil, true, utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "eo_record_not_found"))
		}
		return rec.RecordTypeID, false, nil
	}

	var body eoTypePermissionBody
	if err := c.BodyParser(&body); err != nil {
		return uuid.Nil, false, nil
	}
	id, err := uuid.Parse(body.RecordTypeID)
	if err != nil {
		return uuid.Nil, false, nil
	}
	return id, false, nil
}
