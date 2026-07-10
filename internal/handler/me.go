package handler

import (
	"github.com/gofiber/fiber/v2"

	"imp/internal/apperror"
	"imp/internal/middleware"
	"imp/internal/models"
	"imp/internal/service"
	"imp/pkg/response"
)

// MeHandler hosts self-service endpoints under /me/*. Identity comes from the
// JWT (via middleware.CurrentUserID); there is no :id path parameter so a
// user can never read or mutate another user's preferences here.
type MeHandler struct {
	users *service.UserService
}

func NewMeHandler(users *service.UserService) *MeHandler {
	return &MeHandler{users: users}
}

type notificationPreferencesPayload struct {
	NotifyByEmail bool `json:"notifyByEmail"`
}

// GetNotificationPreferences returns the caller's current preference.
func (h *MeHandler) GetNotificationPreferences(c *fiber.Ctx) error {
	u, err := h.users.Get(c.Context(), middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.OK(c, notificationPreferencesPayload{NotifyByEmail: u.NotifyByEmail})
}

// ChangePassword updates the caller's password after re-verifying their
// current one. 204 on success; existing access tokens stay valid until they
// expire (no global session invalidation).
func (h *MeHandler) ChangePassword(c *fiber.Ctx) error {
	var req models.ChangePasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	if err := h.users.ChangePassword(c.Context(), middleware.CurrentUserID(c), req.Current, req.Next); err != nil {
		return err
	}
	return response.NoContent(c)
}

// UpdateNotificationPreferences toggles email notifications for the caller.
func (h *MeHandler) UpdateNotificationPreferences(c *fiber.Ctx) error {
	var req models.UpdateNotificationPreferencesRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	u, err := h.users.UpdateNotificationPreferences(c.Context(), middleware.CurrentUserID(c), req.NotifyByEmail)
	if err != nil {
		return err
	}
	return response.OK(c, notificationPreferencesPayload{NotifyByEmail: u.NotifyByEmail})
}
