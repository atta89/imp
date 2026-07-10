package handler

import (
	"github.com/gofiber/fiber/v2"

	"imp/internal/apperror"
	"imp/internal/middleware"
	"imp/internal/models"
	"imp/internal/service"
	"imp/pkg/response"
)

type VenueHandler struct {
	venues *service.VenueService
}

func NewVenueHandler(venues *service.VenueService) *VenueHandler {
	return &VenueHandler{venues: venues}
}

func (h *VenueHandler) Create(c *fiber.Ctx) error {
	var req models.CreateVenueRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	v, err := h.venues.Create(c.Context(), req)
	if err != nil {
		return err
	}
	return response.Created(c, v)
}

func (h *VenueHandler) List(c *fiber.Ctx) error {
	page, limit := ParsePagination(c)

	var (
		venues []models.Venue
		total  int64
		err    error
	)
	if middleware.IsAdmin(c) {
		venues, total, err = h.venues.List(c.Context(), page, limit)
	} else {
		// Non-admins see only venues in their scope.
		scope := middleware.CurrentVenueIDs(c)
		ids, perr := parseHexIDs(scope)
		if perr != nil {
			return perr
		}
		venues, total, err = h.venues.ListForUser(c.Context(), ids, page, limit)
	}
	if err != nil {
		return err
	}
	return response.Paginated(c, venues, response.Pagination{
		Page: page, Limit: limit, Total: total, TotalPages: TotalPages(total, limit),
	})
}

func (h *VenueHandler) Get(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	if !middleware.IsAdmin(c) && !middleware.CanAccessVenue(c, id.Hex()) {
		return apperror.Forbidden("not authorized for this venue")
	}
	v, err := h.venues.Get(c.Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, v)
}

func (h *VenueHandler) Update(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	var req models.UpdateVenueRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	v, err := h.venues.Update(c.Context(), id, req)
	if err != nil {
		return err
	}
	return response.OK(c, v)
}

func (h *VenueHandler) Delete(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	if err := h.venues.Delete(c.Context(), id); err != nil {
		return err
	}
	return response.NoContent(c)
}
