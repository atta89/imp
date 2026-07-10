package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"imp/internal/apperror"
)

// CounterRepository implements a monotonic per-name sequence counter used to
// mint human-friendly identifiers like assetTags ("LAP-0001"). One document
// per counter name; $inc is atomic, so two concurrent Next("LAP") calls return
// distinct values.
type CounterRepository struct {
	coll *mongo.Collection
}

func NewCounterRepository(db *mongo.Database) *CounterRepository {
	return &CounterRepository{coll: db.Collection("counters")}
}

func (r *CounterRepository) Next(ctx context.Context, name string) (int64, error) {
	first, err := r.NextN(ctx, name, 1)
	return first, err
}

// NextN atomically reserves a contiguous block of n sequence values in a single
// round-trip and returns the FIRST value of the block (the block is
// [first, first+n-1]). Used by the PO receive / bulk-import flow so minting
// tags for N assets costs one $inc instead of N. n must be >= 1.
func (r *CounterRepository) NextN(ctx context.Context, name string, n int64) (int64, error) {
	if n < 1 {
		n = 1
	}
	var doc struct {
		Seq int64 `bson:"seq"`
	}
	err := r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": name},
		bson.M{"$inc": bson.M{"seq": n}},
		options.FindOneAndUpdate().
			SetUpsert(true).
			SetReturnDocument(options.After),
	).Decode(&doc)
	if err != nil {
		return 0, apperror.Internal("increment counter "+name, err)
	}
	// doc.Seq is the value AFTER incrementing by n, i.e. the last of the block.
	return doc.Seq - n + 1, nil
}
