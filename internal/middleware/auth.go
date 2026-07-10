package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/jwtauth"
	"imp/internal/models"
)

// Context-local keys for the authenticated principal.
const (
	LocalUserID   = "auth.userID"
	LocalRole     = "auth.role"
	LocalVenueIDs = "auth.venueIDs"
)

// RequireAuth verifies the Authorization Bearer access token and stashes
// userID/role/venueIDs into c.Locals for downstream handlers.
func RequireAuth(issuer *jwtauth.Issuer) fiber.Handler {
	return func(c *fiber.Ctx) error {
		raw := c.Get(fiber.HeaderAuthorization)
		const prefix = "Bearer "
		if !strings.HasPrefix(raw, prefix) {
			return apperror.Unauthorized("missing or malformed Authorization header")
		}
		tok := strings.TrimSpace(raw[len(prefix):])
		claims, err := issuer.Parse(tok)
		if err != nil {
			return apperror.Unauthorized("invalid or expired token")
		}
		if claims.Type != jwtauth.TokenAccess {
			return apperror.Unauthorized("not an access token")
		}
		uid, err := claims.SubjectID()
		if err != nil {
			return apperror.Unauthorized("malformed token subject")
		}
		c.Locals(LocalUserID, uid)
		c.Locals(LocalRole, claims.Role)
		c.Locals(LocalVenueIDs, claims.VenueIDs)
		return c.Next()
	}
}

func CurrentRole(c *fiber.Ctx) models.Role {
	r, _ := c.Locals(LocalRole).(models.Role)
	return r
}

func CurrentVenueIDs(c *fiber.Ctx) []string {
	v, _ := c.Locals(LocalVenueIDs).([]string)
	return v
}

func CurrentUserID(c *fiber.Ctx) bson.ObjectID {
	id, _ := c.Locals(LocalUserID).(bson.ObjectID)
	return id
}
