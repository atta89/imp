package handler

import (
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
)

// parseHexIDs converts a slice of hex strings into ObjectIDs. Returns a typed
// BadRequest if any string is malformed (which would indicate a corrupted
// JWT claim).
func parseHexIDs(hex []string) ([]bson.ObjectID, error) {
	out := make([]bson.ObjectID, 0, len(hex))
	for _, h := range hex {
		id, err := bson.ObjectIDFromHex(h)
		if err != nil {
			return nil, apperror.BadRequest("malformed id in token: " + h)
		}
		out = append(out, id)
	}
	return out, nil
}
