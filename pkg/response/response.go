package response

import "github.com/gofiber/fiber/v2"

// Envelope is the standard success response wrapper.
type Envelope struct {
	Data any   `json:"data"`
	Meta *Meta `json:"meta,omitempty"`
}

type Meta struct {
	Pagination *Pagination `json:"pagination,omitempty"`
}

type Pagination struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"totalPages"`
}

// ErrorBody is the standard error response wrapper.
type ErrorBody struct {
	Error ErrorPayload `json:"error"`
}

type ErrorPayload struct {
	Kind    string            `json:"kind"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func OK(c *fiber.Ctx, data any) error {
	return c.Status(fiber.StatusOK).JSON(Envelope{Data: data})
}

func Created(c *fiber.Ctx, data any) error {
	return c.Status(fiber.StatusCreated).JSON(Envelope{Data: data})
}

// Accepted (202) wraps a resource that has been accepted for asynchronous
// processing — e.g. an enqueued BulkJob the caller polls for completion.
func Accepted(c *fiber.Ctx, data any) error {
	return c.Status(fiber.StatusAccepted).JSON(Envelope{Data: data})
}

func Paginated(c *fiber.Ctx, data any, p Pagination) error {
	return c.Status(fiber.StatusOK).JSON(Envelope{Data: data, Meta: &Meta{Pagination: &p}})
}

func NoContent(c *fiber.Ctx) error {
	return c.SendStatus(fiber.StatusNoContent)
}
