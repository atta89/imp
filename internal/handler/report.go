package handler

import (
	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/middleware"
	"imp/internal/service"
	"imp/pkg/response"
)

type ReportHandler struct {
	reports *service.ReportService
}

func NewReportHandler(r *service.ReportService) *ReportHandler {
	return &ReportHandler{reports: r}
}

func (h *ReportHandler) InventoryByVenue(c *fiber.Ctx) error {
	rows, err := h.reports.InventoryByVenue(c.Context())
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

func (h *ReportHandler) AssetsAway(c *fiber.Ctx) error {
	rows, err := h.reports.AssetsAway(c.Context())
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

func (h *ReportHandler) AssetsOverdue(c *fiber.Ctx) error {
	rows, err := h.reports.AssetsOverdue(c.Context())
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

func (h *ReportHandler) InRepair(c *fiber.Ctx) error {
	rows, err := h.reports.InRepair(c.Context())
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

func (h *ReportHandler) ByResponsible(c *fiber.Ctx) error {
	rows, err := h.reports.ByResponsible(c.Context())
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

func (h *ReportHandler) ByDepartment(c *fiber.Ctx) error {
	var venueID *bson.ObjectID
	if v := c.Query("venue"); v != "" {
		id, err := bson.ObjectIDFromHex(v)
		if err != nil {
			return apperror.BadRequest("invalid venue id")
		}
		if !middleware.IsAdmin(c) && !middleware.CanAccessVenue(c, id.Hex()) {
			return apperror.Forbidden("not authorized for this venue")
		}
		venueID = &id
	} else if !middleware.IsAdmin(c) {
		// Non-admins must scope by an accessible venue.
		return apperror.BadRequest("venue query param is required for non-admin callers")
	}
	rows, err := h.reports.ByDepartment(c.Context(), venueID)
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}
