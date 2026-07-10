package middleware

import (
	"log/slog"

	"github.com/gofiber/fiber/v2"

	"imp/internal/apperror"
	"imp/pkg/response"
)

// ErrorHandler maps every returned error to a consistent JSON envelope.
// Install via fiber.Config{ ErrorHandler: middleware.ErrorHandler(logger) }.
func ErrorHandler(logger *slog.Logger) fiber.ErrorHandler {
	return func(c *fiber.Ctx, err error) error {
		if appErr, ok := apperror.As(err); ok {
			if appErr.Kind == apperror.KindInternal {
				logger.Error("internal_error",
					slog.String("path", c.Path()),
					slog.String("message", appErr.Message),
					slog.Any("err", appErr.Err),
				)
			}
			return c.Status(appErr.HTTPStatus()).JSON(response.ErrorBody{
				Error: response.ErrorPayload{
					Kind:    string(appErr.Kind),
					Message: appErr.Message,
					Fields:  appErr.Fields,
				},
			})
		}

		if fiberErr, ok := err.(*fiber.Error); ok {
			return c.Status(fiberErr.Code).JSON(response.ErrorBody{
				Error: response.ErrorPayload{
					Kind:    "http_error",
					Message: fiberErr.Message,
				},
			})
		}

		logger.Error("unhandled_error",
			slog.String("path", c.Path()),
			slog.Any("err", err),
		)
		return c.Status(fiber.StatusInternalServerError).JSON(response.ErrorBody{
			Error: response.ErrorPayload{
				Kind:    string(apperror.KindInternal),
				Message: "internal server error",
			},
		})
	}
}
