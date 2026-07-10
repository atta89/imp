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

type CategoryRepository struct {
	coll *mongo.Collection
}

func NewCategoryRepository(db *mongo.Database) *CategoryRepository {
	return &CategoryRepository{coll: db.Collection("categories")}
}

func (r *CategoryRepository) Create(ctx context.Context, c *models.Category) error {
	if c.ID.IsZero() {
		c.ID = bson.NewObjectID()
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	c.Name = strings.TrimSpace(c.Name)
	c.Slug = strings.ToLower(strings.TrimSpace(c.Slug))
	if c.CustomFields == nil {
		c.CustomFields = []models.CategoryCustomField{}
	}

	if _, err := r.coll.InsertOne(ctx, c); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return apperror.Conflict("category with that slug already exists")
		}
		return apperror.Internal("insert category", err)
	}
	return nil
}

func (r *CategoryRepository) FindByID(ctx context.Context, id bson.ObjectID) (*models.Category, error) {
	var c models.Category
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&c); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("category not found")
		}
		return nil, apperror.Internal("find category", err)
	}
	return &c, nil
}

func (r *CategoryRepository) List(ctx context.Context, page, limit int) ([]models.Category, int64, error) {
	total, err := r.coll.CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, 0, apperror.Internal("count categories", err)
	}
	skip := int64((page - 1) * limit)
	cur, err := r.coll.Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}).
			SetSkip(skip).
			SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, 0, apperror.Internal("list categories", err)
	}
	defer cur.Close(ctx)
	var out []models.Category
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, apperror.Internal("decode categories", err)
	}
	return out, total, nil
}

func (r *CategoryRepository) Update(ctx context.Context, id bson.ObjectID, set bson.M) (*models.Category, error) {
	set["updatedAt"] = time.Now().UTC()
	res := r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": id},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)
	var c models.Category
	if err := res.Decode(&c); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("category not found")
		}
		if mongo.IsDuplicateKeyError(err) {
			return nil, apperror.Conflict("category with that slug already exists")
		}
		return nil, apperror.Internal("update category", err)
	}
	return &c, nil
}

func (r *CategoryRepository) Delete(ctx context.Context, id bson.ObjectID) error {
	res, err := r.coll.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return apperror.Internal("delete category", err)
	}
	if res.DeletedCount == 0 {
		return apperror.NotFound("category not found")
	}
	return nil
}

// FindBySlug resolves a category by its unique slug (lowercased + trimmed —
// matches the normalization applied on Create).
func (r *CategoryRepository) FindBySlug(ctx context.Context, slug string) (*models.Category, error) {
	var c models.Category
	err := r.coll.FindOne(ctx, bson.M{"slug": strings.ToLower(strings.TrimSpace(slug))}).Decode(&c)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("category not found")
		}
		return nil, apperror.Internal("find category by slug", err)
	}
	return &c, nil
}

// FindByName resolves a category by name. Falls back from slug lookups in the
// importer so spreadsheet users can write either form. Case-insensitive,
// trimmed; name is not unique-indexed so duplicates would return the first.
func (r *CategoryRepository) FindByName(ctx context.Context, name string) (*models.Category, error) {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return nil, apperror.NotFound("category not found")
	}
	rx := bson.M{
		"$regex":   "^" + regexQuoteMeta(clean) + "$",
		"$options": "i",
	}
	var c models.Category
	err := r.coll.FindOne(ctx, bson.M{"name": rx}).Decode(&c)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("category not found")
		}
		return nil, apperror.Internal("find category by name", err)
	}
	return &c, nil
}

// regexQuoteMeta escapes Mongo $regex metacharacters in a literal string.
func regexQuoteMeta(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '.', '+', '*', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (r *CategoryRepository) Exists(ctx context.Context, id bson.ObjectID) (bool, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"_id": id})
	if err != nil {
		return false, apperror.Internal("check category exists", err)
	}
	return n > 0, nil
}
