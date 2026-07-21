package handler

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/pagination"
)

const (
	defaultPage  = 1
	defaultLimit = 25
	maxLimit     = 200
)

// ParseObjectID reads a path parameter and returns it as a bson.ObjectID,
// or a typed BadRequest error.
func ParseObjectID(c *fiber.Ctx, name string) (bson.ObjectID, error) {
	raw := c.Params(name)
	id, err := bson.ObjectIDFromHex(raw)
	if err != nil {
		return bson.ObjectID{}, apperror.BadRequest("invalid id: " + raw)
	}
	return id, nil
}

// ParsePagination reads page+limit query params with defaults and bounds.
func ParsePagination(c *fiber.Ctx) (page, limit int) {
	page = defaultPage
	limit = defaultLimit
	if v, err := strconv.Atoi(c.Query("page")); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		if v > maxLimit {
			v = maxLimit
		}
		limit = v
	}
	return page, limit
}

// ParseKeyset reads the keyset-pagination query params: `limit` (default
// defaultLimit, clamped to maxLimit) and `cursor` (opaque token, decoded).
// A malformed cursor yields a typed BadRequest.
func ParseKeyset(c *fiber.Ctx) (*pagination.Cursor, int, error) {
	limit := defaultLimit
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		if v > maxLimit {
			v = maxLimit
		}
		limit = v
	}
	cur, err := pagination.Decode(c.Query("cursor"))
	if err != nil {
		return nil, 0, err
	}
	return cur, limit, nil
}

// TotalPages returns ceil(total/limit).
func TotalPages(total int64, limit int) int {
	if limit <= 0 {
		return 0
	}
	return int((total + int64(limit) - 1) / int64(limit))
}
