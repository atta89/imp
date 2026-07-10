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

type DepartmentRepository struct {
	coll *mongo.Collection
}

func NewDepartmentRepository(db *mongo.Database) *DepartmentRepository {
	return &DepartmentRepository{coll: db.Collection("departments")}
}

func (r *DepartmentRepository) Create(ctx context.Context, d *models.Department) error {
	if d.ID.IsZero() {
		d.ID = bson.NewObjectID()
	}
	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	d.Name = strings.TrimSpace(d.Name)
	d.Code = strings.TrimSpace(d.Code)

	if _, err := r.coll.InsertOne(ctx, d); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return apperror.Conflict("department code already exists for this venue")
		}
		return apperror.Internal("insert department", err)
	}
	return nil
}

func (r *DepartmentRepository) FindByID(ctx context.Context, id bson.ObjectID) (*models.Department, error) {
	var d models.Department
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&d); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("department not found")
		}
		return nil, apperror.Internal("find department", err)
	}
	return &d, nil
}

func (r *DepartmentRepository) ListByVenue(ctx context.Context, venueID bson.ObjectID, page, limit int) ([]models.Department, int64, error) {
	filter := bson.M{"venueId": venueID}
	total, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, apperror.Internal("count departments", err)
	}
	skip := int64((page - 1) * limit)
	cur, err := r.coll.Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}).
			SetSkip(skip).SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, 0, apperror.Internal("list departments", err)
	}
	defer cur.Close(ctx)
	var out []models.Department
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, apperror.Internal("decode departments", err)
	}
	return out, total, nil
}

func (r *DepartmentRepository) Update(ctx context.Context, id bson.ObjectID, set bson.M) (*models.Department, error) {
	set["updatedAt"] = time.Now().UTC()
	res := r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": id},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)
	var d models.Department
	if err := res.Decode(&d); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("department not found")
		}
		if mongo.IsDuplicateKeyError(err) {
			return nil, apperror.Conflict("department code already exists for this venue")
		}
		return nil, apperror.Internal("update department", err)
	}
	return &d, nil
}

func (r *DepartmentRepository) Delete(ctx context.Context, id bson.ObjectID) error {
	res, err := r.coll.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return apperror.Internal("delete department", err)
	}
	if res.DeletedCount == 0 {
		return apperror.NotFound("department not found")
	}
	return nil
}

func (r *DepartmentRepository) FindByVenueAndCode(ctx context.Context, venueID bson.ObjectID, code string) (*models.Department, error) {
	var d models.Department
	err := r.coll.FindOne(ctx, bson.M{"venueId": venueID, "code": strings.TrimSpace(code)}).Decode(&d)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("department not found")
		}
		return nil, apperror.Internal("find department by code", err)
	}
	return &d, nil
}

func (r *DepartmentRepository) FindByIDs(ctx context.Context, ids []bson.ObjectID) ([]models.Department, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cur, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, apperror.Internal("find departments by ids", err)
	}
	defer cur.Close(ctx)
	var out []models.Department
	if err := cur.All(ctx, &out); err != nil {
		return nil, apperror.Internal("decode departments", err)
	}
	return out, nil
}

func (r *DepartmentRepository) CountByVenue(ctx context.Context, venueID bson.ObjectID) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"venueId": venueID})
	if err != nil {
		return 0, apperror.Internal("count departments by venue", err)
	}
	return n, nil
}

func (r *DepartmentRepository) Exists(ctx context.Context, id bson.ObjectID) (bool, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"_id": id})
	if err != nil {
		return false, apperror.Internal("check department exists", err)
	}
	return n > 0, nil
}
