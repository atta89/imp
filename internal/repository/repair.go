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

type RepairRepository struct {
	coll *mongo.Collection
}

func NewRepairRepository(db *mongo.Database) *RepairRepository {
	return &RepairRepository{coll: db.Collection("repairs")}
}

func (r *RepairRepository) Create(ctx context.Context, rep *models.Repair) error {
	if rep.ID.IsZero() {
		rep.ID = bson.NewObjectID()
	}
	now := time.Now().UTC()
	if rep.CreatedAt.IsZero() {
		rep.CreatedAt = now
	}
	rep.UpdatedAt = now
	if rep.ReportedAt.IsZero() {
		rep.ReportedAt = now
	}
	if _, err := r.coll.InsertOne(ctx, rep); err != nil {
		return apperror.Internal("insert repair", err)
	}
	return nil
}

func (r *RepairRepository) FindByID(ctx context.Context, id bson.ObjectID) (*models.Repair, error) {
	var rep models.Repair
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&rep); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("repair not found")
		}
		return nil, apperror.Internal("find repair", err)
	}
	return &rep, nil
}

func (r *RepairRepository) List(ctx context.Context, filter bson.M, page, limit int) ([]models.Repair, int64, error) {
	if filter == nil {
		filter = bson.M{}
	}
	total, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, apperror.Internal("count repairs", err)
	}
	skip := int64((page - 1) * limit)
	cur, err := r.coll.Find(ctx, filter,
		options.Find().
			SetSort(bson.D{{Key: "reportedAt", Value: -1}}).
			SetSkip(skip).
			SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, 0, apperror.Internal("list repairs", err)
	}
	defer cur.Close(ctx)
	var out []models.Repair
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, apperror.Internal("decode repairs", err)
	}
	return out, total, nil
}

func (r *RepairRepository) Update(ctx context.Context, id bson.ObjectID, set bson.M) (*models.Repair, error) {
	set["updatedAt"] = time.Now().UTC()
	res := r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": id},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)
	var rep models.Repair
	if err := res.Decode(&rep); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("repair not found")
		}
		return nil, apperror.Internal("update repair", err)
	}
	return &rep, nil
}
