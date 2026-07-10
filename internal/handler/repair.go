package handler

import (
	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/middleware"
	"imp/internal/models"
	"imp/internal/service"
	"imp/pkg/response"
)

type RepairHandler struct {
	repairs *service.RepairService
	assets  *service.AssetService
}

func NewRepairHandler(repairs *service.RepairService, assets *service.AssetService) *RepairHandler {
	return &RepairHandler{repairs: repairs, assets: assets}
}

func (h *RepairHandler) Create(c *fiber.Ctx) error {
	var req models.CreateRepairRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	// The requester must have access to the asset they're sending to repair.
	a, err := h.assets.Get(c.Context(), req.AssetID)
	if err != nil {
		return err
	}
	if !canAccessAsset(c, a) {
		return apperror.Forbidden("not authorized for this asset")
	}
	rep, _, err := h.repairs.Create(c.Context(), req, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.Created(c, rep)
}

func (h *RepairHandler) List(c *fiber.Ctx) error {
	page, limit := ParsePagination(c)
	q := service.RepairListQuery{}
	if v := c.Query("status"); v != "" {
		q.Status = models.RepairStatus(v)
	}
	if v := c.Query("assetId"); v != "" {
		id, err := bson.ObjectIDFromHex(v)
		if err != nil {
			return apperror.BadRequest("invalid assetId")
		}
		q.AssetID = &id
	}
	reps, total, err := h.repairs.List(c.Context(), q, page, limit)
	if err != nil {
		return err
	}
	return response.Paginated(c, reps, response.Pagination{
		Page: page, Limit: limit, Total: total, TotalPages: TotalPages(total, limit),
	})
}

func (h *RepairHandler) Get(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	rep, err := h.repairs.Get(c.Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, rep)
}

func (h *RepairHandler) Update(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	existing, err := h.repairs.Get(c.Context(), id)
	if err != nil {
		return err
	}
	// Authorize via the underlying asset's venue scope.
	a, err := h.assets.Get(c.Context(), existing.AssetID)
	if err != nil {
		return err
	}
	if !canAccessAsset(c, a) {
		return apperror.Forbidden("not authorized for this asset")
	}
	var req models.UpdateRepairRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	rep, err := h.repairs.Update(c.Context(), id, middleware.CurrentUserID(c), req)
	if err != nil {
		return err
	}
	return response.OK(c, rep)
}
