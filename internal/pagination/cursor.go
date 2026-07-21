// Package pagination encodes and decodes the opaque keyset cursor shared by
// the report list endpoints. A cursor marks a position in a
// (createdAt desc, _id desc) scan: the createdAt and _id of the last row a
// page returned.
package pagination

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
)

// Cursor is a position in a (createdAt desc, _id desc) keyset scan.
type Cursor struct {
	CreatedAt time.Time
	ID        bson.ObjectID
}

// Encode returns an opaque, URL-safe token: base64url("<UnixNano>|<hex _id>").
func Encode(c Cursor) string {
	raw := strconv.FormatInt(c.CreatedAt.UnixNano(), 10) + "|" + c.ID.Hex()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// Decode parses a token produced by Encode. An empty string returns (nil, nil),
// meaning "start from the first page". Malformed input returns a typed
// BadRequest error.
func Decode(raw string) (*Cursor, error) {
	if raw == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, apperror.BadRequest("invalid cursor")
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return nil, apperror.BadRequest("invalid cursor")
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, apperror.BadRequest("invalid cursor")
	}
	id, err := bson.ObjectIDFromHex(parts[1])
	if err != nil {
		return nil, apperror.BadRequest("invalid cursor")
	}
	return &Cursor{CreatedAt: time.Unix(0, nanos).UTC(), ID: id}, nil
}
