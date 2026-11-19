package handlers

import (
	"errors"
	"strings"

	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/i18n"
	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UnitUserHandler resolves the user (driver) assigned to a dispatch unit.
type UnitUserHandler struct {
	dispatch *DispatchProxy
	userRepo repository.UserRepository
}

func NewUnitUserHandler(dispatch *DispatchProxy, userRepo repository.UserRepository) *UnitUserHandler {
	return &UnitUserHandler{dispatch: dispatch, userRepo: userRepo}
}

// GetByUnitID handles GET /auth/unit/:unitId/user (public).
// It asks Registry for the unit's driver and returns a minimal public profile,
// enough for the client to continue with the passcode login.
func (h *UnitUserHandler) GetByUnitID(c *fiber.Ctx) error {
	ctx := c.UserContext()
	unitID := strings.TrimSpace(c.Params("unitId"))
	if unitID == "" || len(unitID) > 100 {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid unit id")
	}

	driverID, err := h.dispatch.FetchUnitDriverID(ctx, unitID)
	if err != nil {
		if errors.Is(err, ErrUnitDriverNotFound) {
			return utils.ErrorResponse(c, fiber.StatusNotFound, "user not found for this unit")
		}
		return utils.ErrorResponse(c, fiber.StatusBadGateway, "dispatch service is unavailable")
	}
	id, err := uuid.Parse(driverID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "user not found for this unit")
	}
	user, err := h.userRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.ErrorResponse(c, fiber.StatusNotFound, "user not found for this unit")
		}
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(ctx, "internal_server_error"))
	}
	isDriver := false
	for _, r := range user.Roles {
		if r.Code == "driver" {
			isDriver = true
			break
		}
	}
	// Only users with the driver role are resolvable through a unit.
	if !user.IsActive || !isDriver {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "user not found for this unit")
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "user fetched", fiber.Map{
		"userId":       user.ID.String(),
		"userName":     user.Username,
		"profileImage": user.Avatar,
	})
}
