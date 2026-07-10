package handler

import (
	"github.com/gofiber/fiber/v2"

	"imp/internal/apperror"
	"imp/internal/middleware"
	"imp/internal/models"
	"imp/internal/service"
	"imp/pkg/response"
)

type UserHandler struct {
	users *service.UserService
}

func NewUserHandler(users *service.UserService) *UserHandler {
	return &UserHandler{users: users}
}

func (h *UserHandler) Create(c *fiber.Ctx) error {
	var req models.CreateUserRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	u, err := h.users.CreateFromRequest(c.Context(), req)
	if err != nil {
		return err
	}
	return response.Created(c, u)
}

func (h *UserHandler) List(c *fiber.Ctx) error {
	page, limit := ParsePagination(c)
	users, total, err := h.users.List(c.Context(), page, limit)
	if err != nil {
		return err
	}
	return response.Paginated(c, users, response.Pagination{
		Page: page, Limit: limit, Total: total, TotalPages: TotalPages(total, limit),
	})
}

func (h *UserHandler) Get(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	u, err := h.users.Get(c.Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, u)
}

func (h *UserHandler) Update(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	var req models.UpdateUserRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	u, err := h.users.Update(c.Context(), id, req)
	if err != nil {
		return err
	}
	return response.OK(c, u)
}

func (h *UserHandler) Delete(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	// Self-delete is dangerous and pointless — block it.
	if id == middleware.CurrentUserID(c) {
		return apperror.BadRequest("cannot delete your own user; another admin must do this")
	}
	if err := h.users.Delete(c.Context(), id); err != nil {
		return err
	}
	return response.NoContent(c)
}
