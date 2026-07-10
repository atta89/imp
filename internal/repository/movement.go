package repository

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"imp/internal/apperror"
	"imp/internal/models"
)

type MovementRepository struct {
	coll *mongo.Collection
}

func NewMovementRepository(db *mongo.Database) *MovementRepository {
	return &MovementRepository{coll: db.Collection("movements")}
}

func (r *MovementRepository) Create(ctx context.Context, m *models.Movement) error {
	if m.ID.IsZero() {
		m.ID = bson.NewObjectID()
	}
	if m.PerformedAt.IsZero() {
		m.PerformedAt = time.Now().UTC()
	}
	if _, err := r.coll.InsertOne(ctx, m); err != nil {
		return apperror.Internal("insert movement", err)
	}
	return nil
}

// movementInsertChunk bounds a single InsertMany payload; see assetInsertChunk.
const movementInsertChunk = 1000

// InsertMany bulk-inserts movements, defaulting IDs and PerformedAt like Create.
// Used by the PO receive / import flow to write per-asset audit movements in
// batches instead of one InsertOne per asset.
func (r *MovementRepository) InsertMany(ctx context.Context, movements []*models.Movement) error {
	if len(movements) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for _, m := range movements {
		if m.ID.IsZero() {
			m.ID = bson.NewObjectID()
		}
		if m.PerformedAt.IsZero() {
			m.PerformedAt = now
		}
	}
	for start := 0; start < len(movements); start += movementInsertChunk {
		end := start + movementInsertChunk
		if end > len(movements) {
			end = len(movements)
		}
		docs := make([]any, 0, end-start)
		for _, m := range movements[start:end] {
			docs = append(docs, m)
		}
		if _, err := r.coll.InsertMany(ctx, docs); err != nil {
			return apperror.Internal("insert movements", err)
		}
	}
	return nil
}

func (r *MovementRepository) ListByAsset(ctx context.Context, assetID bson.ObjectID) ([]models.Movement, error) {
	cur, err := r.coll.Find(ctx,
		bson.M{"assetId": assetID},
		options.Find().SetSort(bson.D{{Key: "performedAt", Value: -1}}),
	)
	if err != nil {
		return nil, apperror.Internal("list movements", err)
	}
	defer cur.Close(ctx)
	var out []models.Movement
	if err := cur.All(ctx, &out); err != nil {
		return nil, apperror.Internal("decode movements", err)
	}
	return out, nil
}
