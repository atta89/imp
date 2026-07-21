package handler

import (
	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/middleware"
	"imp/internal/pagination"
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
	after, limit, err := ParseKeyset(c)
	if err != nil {
		return err
	}
	rows, next, hasMore, err := h.reports.AssetsAway(c.Context(), after, limit)
	if err != nil {
		return err
	}
	return respondCursor(c, rows, next, hasMore, limit)
}

func (h *ReportHandler) AssetsOverdue(c *fiber.Ctx) error {
	after, limit, err := ParseKeyset(c)
	if err != nil {
		return err
	}
	rows, next, hasMore, err := h.reports.AssetsOverdue(c.Context(), after, limit)
	if err != nil {
		return err
	}
	return respondCursor(c, rows, next, hasMore, limit)
}

func (h *ReportHandler) InRepair(c *fiber.Ctx) error {
	after, limit, err := ParseKeyset(c)
	if err != nil {
		return err
	}
	rows, next, hasMore, err := h.reports.InRepair(c.Context(), after, limit)
	if err != nil {
		return err
	}
	return respondCursor(c, rows, next, hasMore, limit)
}

// respondCursor serializes a keyset page: data + meta.cursor, encoding next
// into an opaque token only when there is a further page.
func respondCursor(c *fiber.Ctx, data any, next *pagination.Cursor, hasMore bool, limit int) error {
	page := response.CursorPage{Limit: limit, HasMore: hasMore}
	if next != nil {
		page.NextCursor = pagination.Encode(*next)
	}
	return response.PaginatedCursor(c, data, page)
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
