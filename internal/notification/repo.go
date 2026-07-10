package notification

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"imp/internal/apperror"
	"imp/internal/models"
)

type Repository struct {
	coll *mongo.Collection
}

func NewRepository(db *mongo.Database) *Repository {
	return &Repository{coll: db.Collection("notifications")}
}

func (r *Repository) Create(ctx context.Context, n *models.Notification) error {
	if n.ID.IsZero() {
		n.ID = bson.NewObjectID()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	if n.Status == "" {
		n.Status = models.NotificationStatusQueued
	}
	if n.Channel == "" {
		n.Channel = models.Email
	}
	if _, err := r.coll.InsertOne(ctx, n); err != nil {
		return apperror.Internal("insert notification", err)
	}
	return nil
}

// FindQueued returns up to `limit` queued notifications, oldest first.
func (r *Repository) FindQueued(ctx context.Context, limit int) ([]models.Notification, error) {
	cur, err := r.coll.Find(ctx,
		bson.M{"status": models.NotificationStatusQueued},
		options.Find().
			SetSort(bson.D{{Key: "createdAt", Value: 1}}).
			SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, apperror.Internal("find queued notifications", err)
	}
	defer cur.Close(ctx)
	var out []models.Notification
	if err := cur.All(ctx, &out); err != nil {
		return nil, apperror.Internal("decode queued notifications", err)
	}
	return out, nil
}

func (r *Repository) MarkSent(ctx context.Context, id bson.ObjectID, sentAt time.Time) error {
	_, err := r.coll.UpdateByID(ctx, id, bson.M{
		"$set":   bson.M{"status": models.NotificationStatusSent, "sentAt": sentAt, "error": nil},
		"$unset": bson.M{}, // placeholder; bson.M needs at least one op
	})
	if err != nil {
		return apperror.Internal("mark notification sent", err)
	}
	return nil
}

// RecordFailure increments attempts. If terminal, the status flips to failed
// and the worker won't retry.
func (r *Repository) RecordFailure(ctx context.Context, id bson.ObjectID, attempts int, errMsg string, terminal bool) error {
	set := bson.M{
		"attempts": attempts,
		"error":    errMsg,
	}
	if terminal {
		set["status"] = models.NotificationStatusFailed
	}
	_, err := r.coll.UpdateByID(ctx, id, bson.M{"$set": set})
	if err != nil {
		return apperror.Internal("record notification failure", err)
	}
	return nil
}

// List is for the admin GET /notifications endpoint.
func (r *Repository) List(ctx context.Context, filter bson.M, page, limit int) ([]models.Notification, int64, error) {
	if filter == nil {
		filter = bson.M{}
	}
	total, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, apperror.Internal("count notifications", err)
	}
	skip := int64((page - 1) * limit)
	cur, err := r.coll.Find(ctx, filter,
		options.Find().
			SetSort(bson.D{{Key: "createdAt", Value: -1}}).
			SetSkip(skip).
			SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, 0, apperror.Internal("list notifications", err)
	}
	defer cur.Close(ctx)
	var out []models.Notification
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, apperror.Internal("decode notifications", err)
	}
	return out, total, nil
}
