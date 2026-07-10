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

type PurchaseOrderRepository struct {
	coll *mongo.Collection
}

func NewPurchaseOrderRepository(db *mongo.Database) *PurchaseOrderRepository {
	return &PurchaseOrderRepository{coll: db.Collection("purchase_orders")}
}

func (r *PurchaseOrderRepository) Create(ctx context.Context, po *models.PurchaseOrder) error {
	if po.ID.IsZero() {
		po.ID = bson.NewObjectID()
	}
	now := time.Now().UTC()
	if po.CreatedAt.IsZero() {
		po.CreatedAt = now
	}
	po.UpdatedAt = now
	if _, err := r.coll.InsertOne(ctx, po); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return apperror.Conflict("purchase order with that PO number already exists")
		}
		return apperror.Internal("insert purchase order", err)
	}
	return nil
}

// FindByNumber resolves a PO by its unique poNumber (case-sensitive, trimmed).
func (r *PurchaseOrderRepository) FindByNumber(ctx context.Context, number string) (*models.PurchaseOrder, error) {
	var po models.PurchaseOrder
	if err := r.coll.FindOne(ctx, bson.M{"poNumber": strings.TrimSpace(number)}).Decode(&po); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("purchase order not found")
		}
		return nil, apperror.Internal("find purchase order by number", err)
	}
	return &po, nil
}

// ExistsByNumber returns true if any PO already uses this number. Used by the
// bulk import resolver for the poNumber-conflict check.
func (r *PurchaseOrderRepository) ExistsByNumber(ctx context.Context, number string) (bool, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"poNumber": strings.TrimSpace(number)})
	if err != nil {
		return false, apperror.Internal("count purchase orders by number", err)
	}
	return n > 0, nil
}

func (r *PurchaseOrderRepository) FindByID(ctx context.Context, id bson.ObjectID) (*models.PurchaseOrder, error) {
	var po models.PurchaseOrder
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&po); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("purchase order not found")
		}
		return nil, apperror.Internal("find purchase order", err)
	}
	return &po, nil
}

func (r *PurchaseOrderRepository) List(ctx context.Context, filter bson.M, page, limit int) ([]models.PurchaseOrder, int64, error) {
	if filter == nil {
		filter = bson.M{}
	}
	total, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, apperror.Internal("count purchase orders", err)
	}
	skip := int64((page - 1) * limit)
	cur, err := r.coll.Find(ctx, filter,
		options.Find().
			SetSort(bson.D{{Key: "createdAt", Value: -1}}).
			SetSkip(skip).
			SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, 0, apperror.Internal("list purchase orders", err)
	}
	defer cur.Close(ctx)
	var out []models.PurchaseOrder
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, apperror.Internal("decode purchase orders", err)
	}
	return out, total, nil
}

// CountByResponsibleUser returns the number of POs whose responsibleUserId is
// the given user. Used by user-delete to soft-block when references exist.
func (r *PurchaseOrderRepository) CountByResponsibleUser(ctx context.Context, userID bson.ObjectID) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"responsibleUserId": userID})
	if err != nil {
		return 0, apperror.Internal("count purchase orders by responsible user", err)
	}
	return n, nil
}

func (r *PurchaseOrderRepository) Update(ctx context.Context, id bson.ObjectID, set bson.M) (*models.PurchaseOrder, error) {
	set["updatedAt"] = time.Now().UTC()
	res := r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": id},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)
	var po models.PurchaseOrder
	if err := res.Decode(&po); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("purchase order not found")
		}
		if mongo.IsDuplicateKeyError(err) {
			return nil, apperror.Conflict("purchase order with that PO number already exists")
		}
		return nil, apperror.Internal("update purchase order", err)
	}
	return &po, nil
}
