package handler

import (
	"github.com/gofiber/fiber/v2"

	"imp/internal/apperror"
	"imp/internal/middleware"
	"imp/internal/models"
	"imp/internal/service"
	"imp/pkg/response"
)

type DepartmentHandler struct {
	depts *service.DepartmentService
}

func NewDepartmentHandler(depts *service.DepartmentService) *DepartmentHandler {
	return &DepartmentHandler{depts: depts}
}

func (h *DepartmentHandler) Create(c *fiber.Ctx) error {
	venueID, err := ParseObjectID(c, "venueId")
	if err != nil {
		return err
	}
	if !middleware.IsAdmin(c) && !middleware.CanAccessVenue(c, venueID.Hex()) {
		return apperror.Forbidden("not authorized for this venue")
	}
	var req models.CreateDepartmentRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	d, err := h.depts.Create(c.Context(), venueID, req)
	if err != nil {
		return err
	}
	return response.Created(c, d)
}

func (h *DepartmentHandler) List(c *fiber.Ctx) error {
	venueID, err := ParseObjectID(c, "venueId")
	if err != nil {
		return err
	}
	if !middleware.IsAdmin(c) && !middleware.CanAccessVenue(c, venueID.Hex()) {
		return apperror.Forbidden("not authorized for this venue")
	}
	page, limit := ParsePagination(c)
	depts, total, err := h.depts.List(c.Context(), venueID, page, limit)
	if err != nil {
		return err
	}
	return response.Paginated(c, depts, response.Pagination{
		Page: page, Limit: limit, Total: total, TotalPages: TotalPages(total, limit),
	})
}

func (h *DepartmentHandler) Get(c *fiber.Ctx) error {
	venueID, err := ParseObjectID(c, "venueId")
	if err != nil {
		return err
	}
	if !middleware.IsAdmin(c) && !middleware.CanAccessVenue(c, venueID.Hex()) {
		return apperror.Forbidden("not authorized for this venue")
	}
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	d, err := h.depts.Get(c.Context(), venueID, id)
	if err != nil {
		return err
	}
	return response.OK(c, d)
}

func (h *DepartmentHandler) Update(c *fiber.Ctx) error {
	venueID, err := ParseObjectID(c, "venueId")
	if err != nil {
		return err
	}
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	var req models.UpdateDepartmentRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	d, err := h.depts.Update(c.Context(), venueID, id, req)
	if err != nil {
		return err
	}
	return response.OK(c, d)
}

func (h *DepartmentHandler) Delete(c *fiber.Ctx) error {
	venueID, err := ParseObjectID(c, "venueId")
	if err != nil {
		return err
	}
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	if err := h.depts.Delete(c.Context(), venueID, id); err != nil {
		return err
	}
	return response.NoContent(c)
}
