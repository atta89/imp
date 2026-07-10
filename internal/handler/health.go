package handler

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"imp/pkg/response"
)

type HealthHandler struct {
	mongo *mongo.Client
}

func NewHealthHandler(m *mongo.Client) *HealthHandler {
	return &HealthHandler{mongo: m}
}

// Liveness — process is up.
func (h *HealthHandler) Live(c *fiber.Ctx) error {
	return response.OK(c, fiber.Map{"status": "ok"})
}

// Readiness — process is up AND dependencies (Mongo) are reachable.
func (h *HealthHandler) Ready(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
	defer cancel()
	if err := h.mongo.Ping(ctx, readpref.Primary()); err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"status": "not_ready",
			"mongo":  err.Error(),
		})
	}
	return response.OK(c, fiber.Map{"status": "ready", "mongo": "ok"})
}
