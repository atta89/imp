package handler

import (
	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
	"imp/internal/notification"
	"imp/pkg/response"
)

type NotificationHandler struct {
	outbox *notification.Repository
}

func NewNotificationHandler(outbox *notification.Repository) *NotificationHandler {
	return &NotificationHandler{outbox: outbox}
}

// List is the admin-only outbox log (PRD §9). Filters: status.
func (h *NotificationHandler) List(c *fiber.Ctx) error {
	page, limit := ParsePagination(c)
	filter := bson.M{}
	if v := c.Query("status"); v != "" {
		filter["status"] = models.NotificationStatus(v)
	}
	notifs, total, err := h.outbox.List(c.Context(), filter, page, limit)
	if err != nil {
		return err
	}
	return response.Paginated(c, notifs, response.Pagination{
		Page: page, Limit: limit, Total: total, TotalPages: TotalPages(total, limit),
	})
}
