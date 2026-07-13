package repository

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"imp/internal/apperror"
	"imp/internal/models"
)

type AttachmentRepository struct {
	coll *mongo.Collection
}

func NewAttachmentRepository(db *mongo.Database) *AttachmentRepository {
	return &AttachmentRepository{coll: db.Collection("attachments")}
}

func (r *AttachmentRepository) Insert(ctx context.Context, a *models.Attachment) error {
	if a.ID.IsZero() {
		a.ID = bson.NewObjectID()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if _, err := r.coll.InsertOne(ctx, a); err != nil {
		return apperror.Internal("insert attachment", err)
	}
	return nil
}

func (r *AttachmentRepository) GetByID(ctx context.Context, id bson.ObjectID) (*models.Attachment, error) {
	var a models.Attachment
	err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&a)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("attachment not found")
		}
		return nil, apperror.Internal("get attachment", err)
	}
	return &a, nil
}

// FindByIDs returns any attachments whose _id is in ids. Missing IDs are
// silently omitted; ordering is not guaranteed (callers should build their
// own id→attachment map if they need it).
func (r *AttachmentRepository) FindByIDs(ctx context.Context, ids []bson.ObjectID) ([]models.Attachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cur, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, apperror.Internal("find attachments", err)
	}
	defer cur.Close(ctx)
	var out []models.Attachment
	if err := cur.All(ctx, &out); err != nil {
		return nil, apperror.Internal("decode attachments", err)
	}
	return out, nil
}

// MarkLinked flips linked=true and appends assetID/movementID to the per-doc
// arrays. Idempotent on repeat calls with the same triple. Callers must pass
// a session-scoped ctx if this needs to be inside a transaction — the driver
// picks up the session from the context.
func (r *AttachmentRepository) MarkLinked(ctx context.Context, ids []bson.ObjectID, assetID, movementID bson.ObjectID) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UTC()
	_, err := r.coll.UpdateMany(ctx,
		bson.M{"_id": bson.M{"$in": ids}},
		bson.M{
			"$set": bson.M{
				"linked":   true,
				"linkedAt": now,
			},
			"$addToSet": bson.M{
				"assetIds":    assetID,
				"movementIds": movementID,
			},
		},
	)
	if err != nil {
		return apperror.Internal("mark attachments linked", err)
	}
	return nil
}

// Reserve marks attachments as held by an async bulk job so the orphan sweep
// will not delete them while the job is still queued/running and has not yet
// had a chance to link them inside a batch txn. Fields are internal (bson-only,
// not on the API model). Called at enqueue.
func (r *AttachmentRepository) Reserve(ctx context.Context, ids []bson.ObjectID, jobID bson.ObjectID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.coll.UpdateMany(ctx,
		bson.M{"_id": bson.M{"$in": ids}},
		bson.M{"$set": bson.M{"reservedByJobId": jobID, "reservedAt": time.Now().UTC()}},
	)
	if err != nil {
		return apperror.Internal("reserve attachments", err)
	}
	return nil
}

// ReleaseReservation clears the reservation for a job once it reaches a terminal
// state. Attachments linked to succeeded rows stay linked (and are exempt from
// the sweep on that basis); the attachments of a fully-failed job become
// sweepable again once released.
func (r *AttachmentRepository) ReleaseReservation(ctx context.Context, jobID bson.ObjectID) error {
	_, err := r.coll.UpdateMany(ctx,
		bson.M{"reservedByJobId": jobID},
		bson.M{"$unset": bson.M{"reservedByJobId": "", "reservedAt": ""}},
	)
	if err != nil {
		return apperror.Internal("release attachment reservation", err)
	}
	return nil
}

func (r *AttachmentRepository) ListOrphans(ctx context.Context, olderThan time.Time, limit int64) ([]models.Attachment, error) {
	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}})
	if limit > 0 {
		opts = opts.SetLimit(limit)
	}
	cur, err := r.coll.Find(ctx,
		bson.M{
			"linked":    false,
			"createdAt": bson.M{"$lt": olderThan},
			// Exempt attachments reserved by a not-yet-terminal bulk job.
			"reservedByJobId": bson.M{"$exists": false},
		},
		opts,
	)
	if err != nil {
		return nil, apperror.Internal("list orphans", err)
	}
	defer cur.Close(ctx)
	var out []models.Attachment
	if err := cur.All(ctx, &out); err != nil {
		return nil, apperror.Internal("decode orphans", err)
	}
	return out, nil
}

func (r *AttachmentRepository) Delete(ctx context.Context, id bson.ObjectID) error {
	res, err := r.coll.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return apperror.Internal("delete attachment", err)
	}
	if res.DeletedCount == 0 {
		// Idempotent — a missing doc is not an error.
		return nil
	}
	return nil
}
