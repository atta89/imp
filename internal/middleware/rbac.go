package middleware

import (
	"github.com/gofiber/fiber/v2"

	"imp/internal/apperror"
	"imp/internal/models"
)

// RequireRole permits only requests whose principal has one of the given roles.
// Always install RequireAuth before this.
func RequireRole(allowed ...models.Role) fiber.Handler {
	set := make(map[models.Role]struct{}, len(allowed))
	for _, r := range allowed {
		set[r] = struct{}{}
	}
	return func(c *fiber.Ctx) error {
		role := CurrentRole(c)
		if _, ok := set[role]; !ok {
			return apperror.Forbidden("insufficient role")
		}
		return c.Next()
	}
}

// IsAdmin is a convenience for service-layer venue-scope checks.
func IsAdmin(c *fiber.Ctx) bool { return CurrentRole(c) == models.Admin }

// CanAccessVenue returns true if the principal is an admin or has the venue in
// their assigned scope. Use this in handlers/services that load a resource by
// venue and need to authorize it.
func CanAccessVenue(c *fiber.Ctx, venueID string) bool {
	if IsAdmin(c) {
		return true
	}
	for _, v := range CurrentVenueIDs(c) {
		if v == venueID {
			return true
		}
	}
	return false
}
