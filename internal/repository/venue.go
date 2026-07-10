package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"imp/internal/apperror"
	"imp/internal/models"
)

type VenueRepository struct {
	coll *mongo.Collection
}

func NewVenueRepository(db *mongo.Database) *VenueRepository {
	return &VenueRepository{coll: db.Collection("venues")}
}

func (r *VenueRepository) Create(ctx context.Context, v *models.Venue) error {
	if v.ID.IsZero() {
		v.ID = bson.NewObjectID()
	}
	now := time.Now().UTC()
	if v.CreatedAt.IsZero() {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	v.Code = strings.TrimSpace(v.Code)
	v.Name = strings.TrimSpace(v.Name)

	if _, err := r.coll.InsertOne(ctx, v); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return apperror.Conflict("venue with that code already exists")
		}
		return apperror.Internal("insert venue", err)
	}
	return nil
}

func (r *VenueRepository) FindByID(ctx context.Context, id bson.ObjectID) (*models.Venue, error) {
	var v models.Venue
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&v); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("venue not found")
		}
		return nil, apperror.Internal("find venue", err)
	}
	return &v, nil
}

func (r *VenueRepository) List(ctx context.Context, filter bson.M, page, limit int) ([]models.Venue, int64, error) {
	if filter == nil {
		filter = bson.M{}
	}
	total, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, apperror.Internal("count venues", err)
	}
	skip := int64((page - 1) * limit)
	cur, err := r.coll.Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}).
			SetSkip(skip).
			SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, 0, apperror.Internal("list venues", err)
	}
	defer cur.Close(ctx)
	var out []models.Venue
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, apperror.Internal("decode venues", err)
	}
	return out, total, nil
}

func (r *VenueRepository) Update(ctx context.Context, id bson.ObjectID, set bson.M) (*models.Venue, error) {
	set["updatedAt"] = time.Now().UTC()
	res := r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": id},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)
	var v models.Venue
	if err := res.Decode(&v); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("venue not found")
		}
		if mongo.IsDuplicateKeyError(err) {
			return nil, apperror.Conflict("venue with that code already exists")
		}
		return nil, apperror.Internal("update venue", err)
	}
	return &v, nil
}

func (r *VenueRepository) Delete(ctx context.Context, id bson.ObjectID) error {
	res, err := r.coll.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return apperror.Internal("delete venue", err)
	}
	if res.DeletedCount == 0 {
		return apperror.NotFound("venue not found")
	}
	return nil
}

// FindByIDs batches a multi-id lookup for dashboard/report rendering.
func (r *VenueRepository) FindByIDs(ctx context.Context, ids []bson.ObjectID) ([]models.Venue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cur, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, apperror.Internal("find venues by ids", err)
	}
	defer cur.Close(ctx)
	var out []models.Venue
	if err := cur.All(ctx, &out); err != nil {
		return nil, apperror.Internal("decode venues", err)
	}
	return out, nil
}

// FindByCode resolves a venue by its unique code (case-sensitive, trimmed —
// matches the trimming applied on Create/Update).
func (r *VenueRepository) FindByCode(ctx context.Context, code string) (*models.Venue, error) {
	var v models.Venue
	err := r.coll.FindOne(ctx, bson.M{"code": strings.TrimSpace(code)}).Decode(&v)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("venue not found")
		}
		return nil, apperror.Internal("find venue by code", err)
	}
	return &v, nil
}

func (r *VenueRepository) Exists(ctx context.Context, id bson.ObjectID) (bool, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"_id": id})
	if err != nil {
		return false, apperror.Internal("check venue exists", err)
	}
	return n > 0, nil
}
