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

// awayFromHomeFilter returns the Mongo expression that selects assets whose
// current venue differs from their home venue. Centralized so dashboard,
// reports, and the /assets?away=true query all stay consistent.
func awayFromHomeFilter() bson.M {
	return bson.M{"$expr": bson.M{"$ne": []string{"$homeVenueId", "$currentVenueId"}}}
}

type AssetRepository struct {
	coll *mongo.Collection
}

func NewAssetRepository(db *mongo.Database) *AssetRepository {
	return &AssetRepository{coll: db.Collection("assets")}
}

func (r *AssetRepository) Create(ctx context.Context, a *models.Asset) error {
	if a.ID.IsZero() {
		a.ID = bson.NewObjectID()
	}
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	if _, err := r.coll.InsertOne(ctx, a); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return apperror.Conflict("asset tag or qr token already exists")
		}
		return apperror.Internal("insert asset", err)
	}
	return nil
}

// assetInsertChunk bounds how many assets go into a single InsertMany so a very
// large PO (tens of thousands of assets) can't build one oversized BSON write
// message. The driver also splits internally, but chunking keeps memory and the
// per-write payload predictable.
const assetInsertChunk = 1000

// InsertMany bulk-inserts assets, defaulting IDs and timestamps the same way
// Create does. It replaces the per-asset InsertOne loop in the PO receive flow:
// N inserts collapse to ceil(N/assetInsertChunk) round-trips. A duplicate tag or
// QR token (both unique-indexed) fails the batch — surfaced as a Conflict.
func (r *AssetRepository) InsertMany(ctx context.Context, assets []*models.Asset) error {
	if len(assets) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for _, a := range assets {
		if a.ID.IsZero() {
			a.ID = bson.NewObjectID()
		}
		if a.CreatedAt.IsZero() {
			a.CreatedAt = now
		}
		a.UpdatedAt = now
	}
	for start := 0; start < len(assets); start += assetInsertChunk {
		end := start + assetInsertChunk
		if end > len(assets) {
			end = len(assets)
		}
		docs := make([]any, 0, end-start)
		for _, a := range assets[start:end] {
			docs = append(docs, a)
		}
		if _, err := r.coll.InsertMany(ctx, docs); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return apperror.Conflict("asset tag or qr token already exists")
			}
			return apperror.Internal("insert assets", err)
		}
	}
	return nil
}

// ExistsByTags returns the subset of the given tags that are already used by an
// existing asset, in a single query. Batches the per-asset ExistsByTag check the
// receive flow does for caller-supplied (override) tags.
func (r *AssetRepository) ExistsByTags(ctx context.Context, tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	cur, err := r.coll.Find(ctx,
		bson.M{"assetTag": bson.M{"$in": tags}},
		options.Find().SetProjection(bson.M{"assetTag": 1}),
	)
	if err != nil {
		return nil, apperror.Internal("find assets by tags", err)
	}
	defer cur.Close(ctx)
	var rows []struct {
		AssetTag string `bson:"assetTag"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, apperror.Internal("decode asset tags", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.AssetTag)
	}
	return out, nil
}

func (r *AssetRepository) FindByID(ctx context.Context, id bson.ObjectID) (*models.Asset, error) {
	var a models.Asset
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&a); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("asset not found")
		}
		return nil, apperror.Internal("find asset", err)
	}
	return &a, nil
}

// FindByTag resolves an asset by its unique tag. Used by the bulk importer
// when an admin supplies their own tags in the upload.
func (r *AssetRepository) FindByTag(ctx context.Context, tag string) (*models.Asset, error) {
	var a models.Asset
	if err := r.coll.FindOne(ctx, bson.M{"assetTag": tag}).Decode(&a); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("asset not found")
		}
		return nil, apperror.Internal("find asset by tag", err)
	}
	return &a, nil
}

// ExistsByTag returns true if any asset already uses this tag. Used by the
// bulk import resolver for pre-commit uniqueness checks.
func (r *AssetRepository) ExistsByTag(ctx context.Context, tag string) (bool, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"assetTag": tag})
	if err != nil {
		return false, apperror.Internal("count assets by tag", err)
	}
	return n > 0, nil
}

func (r *AssetRepository) FindByQRToken(ctx context.Context, token string) (*models.Asset, error) {
	var a models.Asset
	if err := r.coll.FindOne(ctx, bson.M{"qrToken": token}).Decode(&a); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("asset not found")
		}
		return nil, apperror.Internal("find asset by qr token", err)
	}
	return &a, nil
}

func (r *AssetRepository) List(ctx context.Context, filter bson.M, page, limit int) ([]models.Asset, int64, error) {
	if filter == nil {
		filter = bson.M{}
	}
	total, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, apperror.Internal("count assets", err)
	}
	skip := int64((page - 1) * limit)
	cur, err := r.coll.Find(ctx, filter,
		options.Find().
			SetSort(bson.D{{Key: "createdAt", Value: -1}}).
			SetSkip(skip).
			SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, 0, apperror.Internal("list assets", err)
	}
	defer cur.Close(ctx)
	var out []models.Asset
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, apperror.Internal("decode assets", err)
	}
	return out, total, nil
}

func (r *AssetRepository) Update(ctx context.Context, id bson.ObjectID, set bson.M) (*models.Asset, error) {
	set["updatedAt"] = time.Now().UTC()
	res := r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": id},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)
	var a models.Asset
	if err := res.Decode(&a); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("asset not found")
		}
		return nil, apperror.Internal("update asset", err)
	}
	return &a, nil
}

func (r *AssetRepository) Delete(ctx context.Context, id bson.ObjectID) error {
	res, err := r.coll.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return apperror.Internal("delete asset", err)
	}
	if res.DeletedCount == 0 {
		return apperror.NotFound("asset not found")
	}
	return nil
}

func (r *AssetRepository) CountByHomeVenue(ctx context.Context, venueID bson.ObjectID) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"homeVenueId": venueID})
	if err != nil {
		return 0, apperror.Internal("count assets by home venue", err)
	}
	return n, nil
}

func (r *AssetRepository) CountByCurrentVenue(ctx context.Context, venueID bson.ObjectID) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"currentVenueId": venueID})
	if err != nil {
		return 0, apperror.Internal("count assets by current venue", err)
	}
	return n, nil
}

func (r *AssetRepository) CountByResponsibleUser(ctx context.Context, userID bson.ObjectID) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"responsibleUserId": userID})
	if err != nil {
		return 0, apperror.Internal("count assets by responsible user", err)
	}
	return n, nil
}

func (r *AssetRepository) CountByCategory(ctx context.Context, categoryID bson.ObjectID) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"categoryId": categoryID})
	if err != nil {
		return 0, apperror.Internal("count assets by category", err)
	}
	return n, nil
}

func (r *AssetRepository) CountAll(ctx context.Context) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{})
	if err != nil {
		return 0, apperror.Internal("count assets", err)
	}
	return n, nil
}

func (r *AssetRepository) CountByStatusValue(ctx context.Context, status models.AssetStatus) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"status": status})
	if err != nil {
		return 0, apperror.Internal("count assets by status", err)
	}
	return n, nil
}

func (r *AssetRepository) CountAwayFromHome(ctx context.Context) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, awayFromHomeFilter())
	if err != nil {
		return 0, apperror.Internal("count assets away", err)
	}
	return n, nil
}

func (r *AssetRepository) CountOverdue(ctx context.Context) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"isOverdue": true})
	if err != nil {
		return 0, apperror.Internal("count overdue assets", err)
	}
	return n, nil
}

func (r *AssetRepository) CountByDepartment(ctx context.Context, departmentID bson.ObjectID) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"departmentId": departmentID})
	if err != nil {
		return 0, apperror.Internal("count assets by department", err)
	}
	return n, nil
}

// AggregateByHomeVenue returns the count of assets per home venue.
func (r *AssetRepository) AggregateByHomeVenue(ctx context.Context) ([]VenueCount, error) {
	cur, err := r.coll.Aggregate(ctx, mongo.Pipeline{
		bson.D{{Key: "$group", Value: bson.M{"_id": "$homeVenueId", "count": bson.M{"$sum": 1}}}},
	})
	if err != nil {
		return nil, apperror.Internal("aggregate assets by venue", err)
	}
	defer cur.Close(ctx)
	var rows []struct {
		ID    bson.ObjectID `bson:"_id"`
		Count int           `bson:"count"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, apperror.Internal("decode venue aggregation", err)
	}
	out := make([]VenueCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, VenueCount{VenueID: r.ID, Count: r.Count})
	}
	return out, nil
}

// AggregateInventoryByVenue returns total + per-status counts for each home venue.
func (r *AssetRepository) AggregateInventoryByVenue(ctx context.Context) ([]VenueInventory, error) {
	cur, err := r.coll.Aggregate(ctx, mongo.Pipeline{
		bson.D{{Key: "$group", Value: bson.M{
			"_id":   bson.M{"venue": "$homeVenueId", "status": "$status"},
			"count": bson.M{"$sum": 1},
		}}},
	})
	if err != nil {
		return nil, apperror.Internal("aggregate inventory by venue", err)
	}
	defer cur.Close(ctx)
	var rows []struct {
		ID struct {
			Venue  bson.ObjectID      `bson:"venue"`
			Status models.AssetStatus `bson:"status"`
		} `bson:"_id"`
		Count int `bson:"count"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, apperror.Internal("decode inventory aggregation", err)
	}
	byVenue := map[bson.ObjectID]*VenueInventory{}
	for _, row := range rows {
		v, ok := byVenue[row.ID.Venue]
		if !ok {
			v = &VenueInventory{VenueID: row.ID.Venue, ByStatus: map[string]int{}}
			byVenue[row.ID.Venue] = v
		}
		v.Total += row.Count
		v.ByStatus[string(row.ID.Status)] += row.Count
	}
	out := make([]VenueInventory, 0, len(byVenue))
	for _, v := range byVenue {
		out = append(out, *v)
	}
	return out, nil
}

// AggregateByResponsible returns the count of assets per responsible user.
// Assets with no custodian are excluded.
func (r *AssetRepository) AggregateByResponsible(ctx context.Context) ([]UserCount, error) {
	cur, err := r.coll.Aggregate(ctx, mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.M{"responsibleUserId": bson.M{"$exists": true, "$ne": nil}}}},
		bson.D{{Key: "$group", Value: bson.M{"_id": "$responsibleUserId", "count": bson.M{"$sum": 1}}}},
	})
	if err != nil {
		return nil, apperror.Internal("aggregate assets by responsible", err)
	}
	defer cur.Close(ctx)
	var rows []struct {
		ID    bson.ObjectID `bson:"_id"`
		Count int           `bson:"count"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, apperror.Internal("decode responsible aggregation", err)
	}
	out := make([]UserCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, UserCount{UserID: r.ID, Count: r.Count})
	}
	return out, nil
}

// AggregateByDepartment returns the count of assets per department. Assets
// without a departmentId are excluded. When venueID != nil the pipeline is
// scoped to that home venue.
func (r *AssetRepository) AggregateByDepartment(ctx context.Context, venueID *bson.ObjectID) ([]DepartmentCount, error) {
	match := bson.M{"departmentId": bson.M{"$exists": true, "$ne": nil}}
	if venueID != nil {
		match["homeVenueId"] = *venueID
	}
	cur, err := r.coll.Aggregate(ctx, mongo.Pipeline{
		bson.D{{Key: "$match", Value: match}},
		bson.D{{Key: "$group", Value: bson.M{
			"_id":   bson.M{"dept": "$departmentId", "venue": "$homeVenueId"},
			"count": bson.M{"$sum": 1},
		}}},
	})
	if err != nil {
		return nil, apperror.Internal("aggregate assets by department", err)
	}
	defer cur.Close(ctx)
	var rows []struct {
		ID struct {
			Dept  bson.ObjectID `bson:"dept"`
			Venue bson.ObjectID `bson:"venue"`
		} `bson:"_id"`
		Count int `bson:"count"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, apperror.Internal("decode department aggregation", err)
	}
	out := make([]DepartmentCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, DepartmentCount{DepartmentID: r.ID.Dept, VenueID: r.ID.Venue, Count: r.Count})
	}
	return out, nil
}

// FindAwayFromHome returns every asset where currentVenueId != homeVenueId.
func (r *AssetRepository) FindAwayFromHome(ctx context.Context) ([]models.Asset, error) {
	return r.find(ctx, awayFromHomeFilter())
}

// FindOverdue returns every asset with isOverdue=true.
func (r *AssetRepository) FindOverdue(ctx context.Context) ([]models.Asset, error) {
	return r.find(ctx, bson.M{"isOverdue": true})
}

// FindOverdueWithCustodian returns the same set, restricted to assets that
// have a responsibleUserId — the only ones that produce a digest recipient.
func (r *AssetRepository) FindOverdueWithCustodian(ctx context.Context) ([]models.Asset, error) {
	return r.find(ctx, bson.M{
		"isOverdue":         true,
		"responsibleUserId": bson.M{"$exists": true, "$ne": nil},
	})
}

func (r *AssetRepository) find(ctx context.Context, filter bson.M) ([]models.Asset, error) {
	cur, err := r.coll.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "assetTag", Value: 1}}))
	if err != nil {
		return nil, apperror.Internal("find assets", err)
	}
	defer cur.Close(ctx)
	var out []models.Asset
	if err := cur.All(ctx, &out); err != nil {
		return nil, apperror.Internal("decode assets", err)
	}
	return out, nil
}

// FlagOverdue marks every asset whose expectedReturnDate is < asOf and that
// isn't already flagged. Returns the number of newly-flagged assets so the
// caller can log a meaningful daily-scan summary.
func (r *AssetRepository) FlagOverdue(ctx context.Context, asOf time.Time) (int64, error) {
	res, err := r.coll.UpdateMany(ctx,
		bson.M{
			"expectedReturnDate": bson.M{"$lt": asOf},
			"isOverdue":          false,
			"isActive":           true,
		},
		bson.M{"$set": bson.M{"isOverdue": true, "updatedAt": time.Now().UTC()}},
	)
	if err != nil {
		return 0, apperror.Internal("flag overdue assets", err)
	}
	return res.ModifiedCount, nil
}

// VenueCount, VenueInventory, UserCount are aggregation result rows. Kept
// in the repository package so the service layer doesn't depend on Mongo
// types but still gets a typed result.
// FindByIDs returns assets matching the given ids, ordered by the input
// slice. Returns apperror.NotFound if any id is missing — callers print the
// missing ids verbatim.
func (r *AssetRepository) FindByIDs(ctx context.Context, ids []bson.ObjectID) ([]models.Asset, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cur, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, apperror.Internal("find assets by ids", err)
	}
	defer cur.Close(ctx)
	var rows []models.Asset
	if err := cur.All(ctx, &rows); err != nil {
		return nil, apperror.Internal("decode assets", err)
	}
	if len(rows) != len(ids) {
		byID := make(map[bson.ObjectID]struct{}, len(rows))
		for _, r := range rows {
			byID[r.ID] = struct{}{}
		}
		for _, id := range ids {
			if _, ok := byID[id]; !ok {
				return nil, apperror.NotFound("asset not found: " + id.Hex())
			}
		}
	}
	// Re-order to match input.
	pos := make(map[bson.ObjectID]int, len(ids))
	for i, id := range ids {
		pos[id] = i
	}
	out := make([]models.Asset, len(ids))
	for _, r := range rows {
		out[pos[r.ID]] = r
	}
	return out, nil
}

type VenueCount struct {
	VenueID bson.ObjectID
	Count   int
}

type VenueInventory struct {
	VenueID  bson.ObjectID
	Total    int
	ByStatus map[string]int
}

type UserCount struct {
	UserID bson.ObjectID
	Count  int
}

// DepartmentCount is the per-department aggregation row. VenueID is preserved
// so the caller can hydrate the venue label without a second aggregation.
type DepartmentCount struct {
	DepartmentID bson.ObjectID
	VenueID      bson.ObjectID
	Count        int
}
