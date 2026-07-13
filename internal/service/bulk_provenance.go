package service

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
)

// bulkJobIDKey carries the id of the async bulk job whose batch is currently
// executing. It is threaded through the transaction context so the SHARED
// apply* helpers can stamp their Movement with bulkJobId provenance without
// changing their signatures or the single-asset call sites. Single-asset
// actions never set it, so their movements are byte-identical to before
// (bulkJobId is omitempty).
type bulkJobIDKey struct{}

// withBulkJobID returns a context that stamps movements written under it with
// the given bulk job id. Used by the batch executor.
func withBulkJobID(ctx context.Context, id bson.ObjectID) context.Context {
	return context.WithValue(ctx, bulkJobIDKey{}, id)
}

// stampBulkJobID sets m.BulkJobID when the context carries a bulk job id.
// Called by every apply* helper just before persisting the movement.
func stampBulkJobID(ctx context.Context, m *models.Movement) {
	if v, ok := ctx.Value(bulkJobIDKey{}).(bson.ObjectID); ok && !v.IsZero() {
		id := v
		m.BulkJobID = &id
	}
}
