package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"imp/internal/apperror"
	"imp/internal/models"
)

type UserRepository struct {
	coll *mongo.Collection
}

func NewUserRepository(db *mongo.Database) *UserRepository {
	return &UserRepository{coll: db.Collection("users")}
}

func (r *UserRepository) Create(ctx context.Context, u *models.User) error {
	if u.ID.IsZero() {
		u.ID = bson.NewObjectID()
	}
	now := time.Now().UTC()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	u.UpdatedAt = now
	u.Email = openapi_types.Email(strings.ToLower(strings.TrimSpace(string(u.Email))))

	if _, err := r.coll.InsertOne(ctx, u); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return apperror.Conflict("user with that email already exists")
		}
		return apperror.Internal("insert user", err)
	}
	return nil
}

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (*models.User, error) {
	var u models.User
	err := r.coll.FindOne(ctx, bson.M{"email": strings.ToLower(strings.TrimSpace(email))}).Decode(&u)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("user not found")
		}
		return nil, apperror.Internal("find user by email", err)
	}
	return &u, nil
}

func (r *UserRepository) FindByID(ctx context.Context, id bson.ObjectID) (*models.User, error) {
	var u models.User
	err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&u)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("user not found")
		}
		return nil, apperror.Internal("find user by id", err)
	}
	return &u, nil
}

func (r *UserRepository) ExistsByRole(ctx context.Context, role models.Role) (bool, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"role": role})
	if err != nil {
		return false, apperror.Internal("count users by role", err)
	}
	return n > 0, nil
}

func (r *UserRepository) List(ctx context.Context, filter bson.M, page, limit int) ([]models.User, int64, error) {
	if filter == nil {
		filter = bson.M{}
	}
	total, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, apperror.Internal("count users", err)
	}
	skip := int64((page - 1) * limit)
	cur, err := r.coll.Find(ctx, filter,
		options.Find().
			SetSort(bson.D{{Key: "name", Value: 1}}).
			SetSkip(skip).
			SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, 0, apperror.Internal("list users", err)
	}
	defer cur.Close(ctx)
	var out []models.User
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, apperror.Internal("decode users", err)
	}
	return out, total, nil
}

func (r *UserRepository) Update(ctx context.Context, id bson.ObjectID, set bson.M) (*models.User, error) {
	set["updatedAt"] = time.Now().UTC()
	res := r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": id},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)
	var u models.User
	if err := res.Decode(&u); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("user not found")
		}
		if mongo.IsDuplicateKeyError(err) {
			return nil, apperror.Conflict("user with that email already exists")
		}
		return nil, apperror.Internal("update user", err)
	}
	return &u, nil
}

func (r *UserRepository) Delete(ctx context.Context, id bson.ObjectID) error {
	res, err := r.coll.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return apperror.Internal("delete user", err)
	}
	if res.DeletedCount == 0 {
		return apperror.NotFound("user not found")
	}
	return nil
}

// FindByIDs batches a multi-id user lookup (for reports + digest rendering).
func (r *UserRepository) FindByIDs(ctx context.Context, ids []bson.ObjectID) ([]models.User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cur, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, apperror.Internal("find users by ids", err)
	}
	defer cur.Close(ctx)
	var out []models.User
	if err := cur.All(ctx, &out); err != nil {
		return nil, apperror.Internal("decode users", err)
	}
	return out, nil
}

// FindManagersForVenue returns active venue_manager users assigned to the
// given venue. Used by the transfer notification trigger.
func (r *UserRepository) FindManagersForVenue(ctx context.Context, venueID bson.ObjectID) ([]models.User, error) {
	cur, err := r.coll.Find(ctx, bson.M{
		"role":     models.VenueManager,
		"venueIds": venueID,
		"isActive": true,
	})
	if err != nil {
		return nil, apperror.Internal("find managers for venue", err)
	}
	defer cur.Close(ctx)
	var out []models.User
	if err := cur.All(ctx, &out); err != nil {
		return nil, apperror.Internal("decode managers", err)
	}
	return out, nil
}

// Touch updates UpdatedAt only — useful as a sanity check in tests.
func (r *UserRepository) Touch(ctx context.Context, id bson.ObjectID) error {
	res, err := r.coll.UpdateByID(ctx, id, bson.M{"$set": bson.M{"updatedAt": time.Now().UTC()}})
	if err != nil {
		return apperror.Internal("touch user", err)
	}
	if res.MatchedCount == 0 {
		return apperror.NotFound(fmt.Sprintf("user %s not found", id.Hex()))
	}
	return nil
}
