package handler

import (
	"github.com/gofiber/fiber/v2"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/service"
	"imp/pkg/response"
)

type CategoryHandler struct {
	categories *service.CategoryService
}

func NewCategoryHandler(categories *service.CategoryService) *CategoryHandler {
	return &CategoryHandler{categories: categories}
}

func (h *CategoryHandler) Create(c *fiber.Ctx) error {
	var req models.CreateCategoryRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	cat, err := h.categories.Create(c.Context(), req)
	if err != nil {
		return err
	}
	return response.Created(c, cat)
}

func (h *CategoryHandler) List(c *fiber.Ctx) error {
	page, limit := ParsePagination(c)
	cats, total, err := h.categories.List(c.Context(), page, limit)
	if err != nil {
		return err
	}
	return response.Paginated(c, cats, response.Pagination{
		Page: page, Limit: limit, Total: total, TotalPages: TotalPages(total, limit),
	})
}

func (h *CategoryHandler) Get(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	cat, err := h.categories.Get(c.Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, cat)
}

func (h *CategoryHandler) Update(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	var req models.UpdateCategoryRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	cat, err := h.categories.Update(c.Context(), id, req)
	if err != nil {
		return err
	}
	return response.OK(c, cat)
}

func (h *CategoryHandler) Delete(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	if err := h.categories.Delete(c.Context(), id); err != nil {
		return err
	}
	return response.NoContent(c)
}
