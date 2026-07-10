package handler

import (
	"github.com/gofiber/fiber/v2"

	"imp/internal/service"
	"imp/pkg/response"
)

type DashboardHandler struct {
	dashboard *service.DashboardService
}

func NewDashboardHandler(d *service.DashboardService) *DashboardHandler {
	return &DashboardHandler{dashboard: d}
}

func (h *DashboardHandler) Summary(c *fiber.Ctx) error {
	s, err := h.dashboard.Summary(c.Context())
	if err != nil {
		return err
	}
	return response.OK(c, s)
}
