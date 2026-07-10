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

// ImportJobRepository persists bulk-import job state (PRD §6.12). The collection
// is the source of truth for an import's status, counts, errors, and the parsed
// payload (so commit can replay validate without re-parsing the file).
type ImportJobRepository struct {
	coll *mongo.Collection
}

func NewImportJobRepository(db *mongo.Database) *ImportJobRepository {
	return &ImportJobRepository{coll: db.Collection("import_jobs")}
}

// ImportJobDoc is the on-disk shape: the public ImportJob plus the internal
// ParsedRows snapshot. ParsedRows is never serialized over the API — keep it
// off models.ImportJob.
type ImportJobDoc struct {
	models.ImportJob `bson:",inline"`
	ParsedRows       []models.ImportRow `bson:"parsedRows,omitempty"`
}

// CountsDelta increments specific counters atomically. Zero-valued fields are
// not written; use this from the commit loop after each PO succeeds or fails.
type CountsDelta struct {
	PosCreated    int
	AssetsCreated int
	RowsSkipped   int
	RowsErrored   int
}

func (r *ImportJobRepository) Create(ctx context.Context, doc *ImportJobDoc) error {
	if doc.ID.IsZero() {
		doc.ID = bson.NewObjectID()
	}
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now().UTC()
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		return apperror.Internal("insert import job", err)
	}
	return nil
}

func (r *ImportJobRepository) FindByID(ctx context.Context, id bson.ObjectID) (*ImportJobDoc, error) {
	var doc ImportJobDoc
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("import job not found")
		}
		return nil, apperror.Internal("find import job", err)
	}
	return &doc, nil
}

// Update applies a $set patch and returns the updated doc.
func (r *ImportJobRepository) Update(ctx context.Context, id bson.ObjectID, set bson.M) (*ImportJobDoc, error) {
	res := r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": id},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)
	var doc ImportJobDoc
	if err := res.Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("import job not found")
		}
		return nil, apperror.Internal("update import job", err)
	}
	return &doc, nil
}

// SaveParsedRows replaces the parsedRows snapshot wholesale. Called once at
// the end of Validate. Commit reads them back to avoid re-parsing.
func (r *ImportJobRepository) SaveParsedRows(ctx context.Context, id bson.ObjectID, rows []models.ImportRow) error {
	if _, err := r.coll.UpdateByID(ctx, id, bson.M{"$set": bson.M{"parsedRows": rows}}); err != nil {
		return apperror.Internal("save parsed rows", err)
	}
	return nil
}

// SetCounts writes the full counts struct (used by Validate after resolving).
func (r *ImportJobRepository) SetCounts(ctx context.Context, id bson.ObjectID, counts models.ImportJobCounts) error {
	if _, err := r.coll.UpdateByID(ctx, id, bson.M{"$set": bson.M{"counts": counts}}); err != nil {
		return apperror.Internal("set import counts", err)
	}
	return nil
}

// IncrementCounts adds the delta atomically. Used inside the commit loop.
func (r *ImportJobRepository) IncrementCounts(ctx context.Context, id bson.ObjectID, d CountsDelta) error {
	inc := bson.M{}
	if d.PosCreated != 0 {
		inc["counts.posCreated"] = d.PosCreated
	}
	if d.AssetsCreated != 0 {
		inc["counts.assetsCreated"] = d.AssetsCreated
	}
	if d.RowsSkipped != 0 {
		inc["counts.rowsSkipped"] = d.RowsSkipped
	}
	if d.RowsErrored != 0 {
		inc["counts.rowsErrored"] = d.RowsErrored
	}
	if len(inc) == 0 {
		return nil
	}
	if _, err := r.coll.UpdateByID(ctx, id, bson.M{"$inc": inc}); err != nil {
		return apperror.Internal("increment import counts", err)
	}
	return nil
}

// AppendErrors $push's additional row-level errors onto the job. Used inside
// the commit loop when a PO fails mid-import.
func (r *ImportJobRepository) AppendErrors(ctx context.Context, id bson.ObjectID, errs []models.ImportRowError) error {
	if len(errs) == 0 {
		return nil
	}
	if _, err := r.coll.UpdateByID(ctx, id, bson.M{
		"$push": bson.M{"errors": bson.M{"$each": errs}},
	}); err != nil {
		return apperror.Internal("append import errors", err)
	}
	return nil
}
