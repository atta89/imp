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

type PurchaseOrderHandler struct {
	pos *service.PurchaseOrderService
}

func NewPurchaseOrderHandler(pos *service.PurchaseOrderService) *PurchaseOrderHandler {
	return &PurchaseOrderHandler{pos: pos}
}

func (h *PurchaseOrderHandler) Create(c *fiber.Ctx) error {
	var req models.CreatePurchaseOrderRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	po, err := h.pos.Create(c.Context(), req, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.Created(c, po)
}

func (h *PurchaseOrderHandler) List(c *fiber.Ctx) error {
	page, limit := ParsePagination(c)
	q := service.PurchaseOrderListQuery{}
	if v := c.Query("status"); v != "" {
		q.Status = models.PurchaseOrderStatus(v)
	}
	if v := c.Query("responsible"); v != "" {
		id, err := bson.ObjectIDFromHex(v)
		if err != nil {
			return apperror.BadRequest("invalid responsible id")
		}
		q.Responsible = &id
	}
	pos, total, err := h.pos.List(c.Context(), q, page, limit)
	if err != nil {
		return err
	}
	return response.Paginated(c, pos, response.Pagination{
		Page: page, Limit: limit, Total: total, TotalPages: TotalPages(total, limit),
	})
}

func (h *PurchaseOrderHandler) Get(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	po, err := h.pos.Get(c.Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, po)
}

func (h *PurchaseOrderHandler) Update(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	var req models.UpdatePurchaseOrderRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	po, err := h.pos.Update(c.Context(), id, req)
	if err != nil {
		return err
	}
	return response.OK(c, po)
}

func (h *PurchaseOrderHandler) Receive(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	var req models.ReceivePurchaseOrderRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	if !middleware.IsAdmin(c) && !middleware.CanAccessVenue(c, req.VenueID.Hex()) {
		return apperror.Forbidden("not authorized for destination venue")
	}
	res, err := h.pos.Receive(c.Context(), id, req)
	if err != nil {
		return err
	}
	return response.OK(c, res)
}
