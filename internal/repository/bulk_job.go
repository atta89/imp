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

// ErrLeaseLost is returned by ApplyBatchResult, InitIDsScan, and
// AdvanceIDsScan when the conditional job-doc update matches nothing — i.e.
// another worker reclaimed an expired lease and advanced the cursor. The
// caller MUST abort the surrounding transaction so no asset/movement writes
// commit; the reclaiming worker owns the batch.
var ErrLeaseLost = errors.New("bulk job lease lost or cursor advanced by another worker")

// BulkJobRepository persists async bulk-asset job state. The collection is the
// source of truth for a job's status, counts, progress cursor, worker lease,
// and (for qr jobs) the rendered-PDF storage key.
type BulkJobRepository struct {
	coll *mongo.Collection
}

func NewBulkJobRepository(db *mongo.Database) *BulkJobRepository {
	return &BulkJobRepository{coll: db.Collection("bulk_jobs")}
}

// BulkJobParams is the superset of per-action parameters. Only the fields
// relevant to the job's type are set. Validated at enqueue; re-checked per row
// inside each batch transaction.
type BulkJobParams struct {
	ToVenueID          *bson.ObjectID         `bson:"toVenueId,omitempty"`
	ExpectedReturnDate *time.Time             `bson:"expectedReturnDate,omitempty"`
	Status             *models.AssetStatus    `bson:"status,omitempty"`
	Reason             *string                `bson:"reason,omitempty"`
	ResponsibleUserID  *bson.ObjectID         `bson:"responsibleUserId,omitempty"`
	Condition          *models.AssetCondition `bson:"condition,omitempty"`
	Notes              *string                `bson:"notes,omitempty"`

	// IDs holds the persisted filter + venue scope + cap for an ids-export job,
	// so the worker can rebuild the exact GET /assets query (Fiber-free) off the
	// job doc after a crash/reclaim.
	IDs *IDsParams `bson:"ids,omitempty"`
}

// IDsParams is the durable form of an AssetListQuery for an ids-export job.
// Scoped/ScopeVenueIDs preserve the list endpoint's nil-vs-empty scope semantics:
// Scoped=false ⇒ admin/unrestricted; Scoped=true ⇒ restrict to ScopeVenueIDs
// (an empty slice legitimately matches nothing).
type IDsParams struct {
	Venue         *bson.ObjectID      `bson:"venue,omitempty"`
	CurrentVenue  *bson.ObjectID      `bson:"currentVenue,omitempty"`
	Category      *bson.ObjectID      `bson:"category,omitempty"`
	Department    *bson.ObjectID      `bson:"department,omitempty"`
	Responsible   *bson.ObjectID      `bson:"responsible,omitempty"`
	Status        *models.AssetStatus `bson:"status,omitempty"`
	Away          bool                `bson:"away,omitempty"`
	Overdue       bool                `bson:"overdue,omitempty"`
	Q             string              `bson:"q,omitempty"`
	Limit         int                 `bson:"limit"`
	Scoped        bool                `bson:"scoped"`
	ScopeVenueIDs []bson.ObjectID     `bson:"scopeVenueIds,omitempty"`
}

// BulkJobDoc is the on-disk shape: the API-visible BulkJob plus the internal
// execution state (params, asset/attachment sets, cursor, lease, result key).
// The internal fields are never serialized over the API — keep them off
// models.BulkJob.
type BulkJobDoc struct {
	models.BulkJob `bson:",inline"`

	Params        BulkJobParams   `bson:"params"`
	AssetIDs      []bson.ObjectID `bson:"assetIds"`
	AttachmentIDs []bson.ObjectID `bson:"attachmentIds,omitempty"`
	BatchSize     int             `bson:"batchSize"`

	// Cursor is the index of the next unprocessed asset in AssetIDs. It is
	// advanced ONLY inside a committed batch transaction, so a crash + reclaim
	// can never double-apply a batch.
	Cursor int `bson:"cursor"`

	// Worker lease.
	ClaimedBy      string     `bson:"claimedBy,omitempty"`
	ClaimedAt      *time.Time `bson:"claimedAt,omitempty"`
	LeaseExpiresAt *time.Time `bson:"leaseExpiresAt,omitempty"`
	Attempts       int        `bson:"attempts"`
	LastError      string     `bson:"lastError,omitempty"`

	// qr jobs only: FileStorage key of the rendered PDF. Never exposed raw
	// over the API. Empty on a completed qr job means the result was cleaned
	// up after BULK_RESULT_TTL_DAYS (→ 410 Gone).
	ResultStorageKey string `bson:"resultStorageKey,omitempty"`

	// NotifiedAt marks that the completion digest step has run, so it can never
	// double-send even if completion is retried.
	NotifiedAt *time.Time `bson:"notifiedAt,omitempty"`
}

func (r *BulkJobRepository) Create(ctx context.Context, doc *BulkJobDoc) error {
	if doc.ID.IsZero() {
		doc.ID = bson.NewObjectID()
	}
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now().UTC()
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		return apperror.Internal("insert bulk job", err)
	}
	return nil
}

func (r *BulkJobRepository) FindByID(ctx context.Context, id bson.ObjectID) (*BulkJobDoc, error) {
	var doc BulkJobDoc
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperror.NotFound("bulk job not found")
		}
		return nil, apperror.Internal("find bulk job", err)
	}
	return &doc, nil
}

// List returns jobs filtered by optional status/type, newest first. Used by the
// admin list endpoint.
func (r *BulkJobRepository) List(ctx context.Context, status *models.BulkJobStatus, typ *models.BulkJobType, limit int64) ([]BulkJobDoc, error) {
	filter := bson.M{}
	if status != nil {
		filter["status"] = *status
	}
	if typ != nil {
		filter["type"] = *typ
	}
	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}})
	if limit > 0 {
		opts.SetLimit(limit)
	}
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, apperror.Internal("list bulk jobs", err)
	}
	var docs []BulkJobDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, apperror.Internal("decode bulk jobs", err)
	}
	return docs, nil
}

// Claim atomically leases the oldest runnable job to workerID: a queued job, or
// a running job whose lease has expired (crash recovery). Returns (nil, nil)
// when nothing is claimable. On claim it flips status→running, stamps the
// lease, sets startedAt on first run, and bumps attempts.
func (r *BulkJobRepository) Claim(ctx context.Context, workerID string, now time.Time, lease time.Duration) (*BulkJobDoc, error) {
	expiresAt := now.Add(lease)
	filter := bson.M{"$or": bson.A{
		bson.M{"status": models.BulkJobStatusQueued},
		bson.M{"status": models.BulkJobStatusRunning, "leaseExpiresAt": bson.M{"$lt": now}},
	}}
	update := bson.M{
		"$set": bson.M{
			"status":         models.BulkJobStatusRunning,
			"claimedBy":      workerID,
			"claimedAt":      now,
			"leaseExpiresAt": expiresAt,
		},
		"$inc": bson.M{"attempts": 1},
	}
	opts := options.FindOneAndUpdate().
		SetSort(bson.D{{Key: "createdAt", Value: 1}}).
		SetReturnDocument(options.After)

	res := r.coll.FindOneAndUpdate(ctx, filter, update, opts)
	var doc BulkJobDoc
	if err := res.Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, apperror.Internal("claim bulk job", err)
	}
	// Stamp startedAt once, outside the atomic claim (idempotent).
	if doc.StartedAt == nil {
		started := now
		if _, err := r.coll.UpdateOne(ctx,
			bson.M{"_id": doc.ID, "startedAt": bson.M{"$exists": false}},
			bson.M{"$set": bson.M{"startedAt": started}},
		); err != nil {
			return nil, apperror.Internal("stamp startedAt", err)
		}
		doc.StartedAt = &started
	}
	return &doc, nil
}

// ExtendLease pushes the lease expiry forward. Called after each committed
// batch so a long job isn't reclaimed mid-flight. Guarded on claimedBy so a
// worker that already lost its lease cannot resurrect it.
func (r *BulkJobRepository) ExtendLease(ctx context.Context, id bson.ObjectID, workerID string, now time.Time, lease time.Duration) error {
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id, "claimedBy": workerID},
		bson.M{"$set": bson.M{"leaseExpiresAt": now.Add(lease)}},
	)
	if err != nil {
		return apperror.Internal("extend bulk job lease", err)
	}
	return nil
}

// BatchUpdate is the durable result of one batch, applied to the job doc inside
// the SAME transaction as the batch's asset/movement/attachment writes.
type BatchUpdate struct {
	NewCursor          int
	SucceededInc       int
	FailedInc          int
	SkippedInc         int
	NewErrors          []models.BulkJobRowError
	SetErrorsTruncated bool
}

// ApplyBatchResult advances the cursor, increments counts, bumps
// progress.batchesDone, and appends (capped) row errors — all in one update,
// conditional on the worker still holding the lease AND the cursor being where
// this worker left it. If the condition matches nothing, another worker
// reclaimed the job and advanced past this batch: it returns ErrLeaseLost so
// the caller aborts the transaction. Because this update lives in the batch
// txn, cursor/counts advance atomically with the work.
func (r *BulkJobRepository) ApplyBatchResult(ctx context.Context, id bson.ObjectID, workerID string, expectedCursor int, upd BatchUpdate) error {
	set := bson.M{"cursor": upd.NewCursor}
	if upd.SetErrorsTruncated {
		set["errorsTruncated"] = true
	}
	inc := bson.M{"progress.batchesDone": 1}
	if upd.SucceededInc != 0 {
		inc["counts.succeeded"] = upd.SucceededInc
	}
	if upd.FailedInc != 0 {
		inc["counts.failed"] = upd.FailedInc
	}
	if upd.SkippedInc != 0 {
		inc["counts.skipped"] = upd.SkippedInc
	}
	update := bson.M{"$set": set, "$inc": inc}
	if len(upd.NewErrors) > 0 {
		update["$push"] = bson.M{"errors": bson.M{"$each": upd.NewErrors}}
	}

	res, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id, "claimedBy": workerID, "cursor": expectedCursor},
		update,
	)
	if err != nil {
		return apperror.Internal("apply bulk batch result", err)
	}
	if res.MatchedCount == 0 {
		return ErrLeaseLost
	}
	return nil
}

// InitIDsScan (re)initializes an ids-export job's counters at the start of a
// run: it sets the now-known matched total and batchesTotal, resets the running
// counters to zero (so a crash + reclaim restarts cleanly from zero — the scan
// is read-only and cheap), and extends the lease. Guarded on claimedBy;
// ErrLeaseLost if the worker no longer owns the job.
func (r *BulkJobRepository) InitIDsScan(ctx context.Context, id bson.ObjectID, workerID string, total, batchesTotal int, now time.Time, lease time.Duration) error {
	res, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id, "claimedBy": workerID},
		bson.M{"$set": bson.M{
			"counts.total":          total,
			"counts.succeeded":      0,
			"counts.failed":         0,
			"counts.skipped":        0,
			"progress.batchesTotal": batchesTotal,
			"progress.batchesDone":  0,
			"cursor":                0,
			"leaseExpiresAt":        now.Add(lease),
		}},
	)
	if err != nil {
		return apperror.Internal("init ids scan", err)
	}
	if res.MatchedCount == 0 {
		return ErrLeaseLost
	}
	return nil
}

// AdvanceIDsScan records progress after a keyset batch: sets the collected count
// as succeeded, bumps batchesDone, and heartbeats the lease — in one guarded
// update. Guarded on claimedBy so a worker that lost its lease cannot overwrite
// the reclaiming worker's progress; ErrLeaseLost when unmatched.
func (r *BulkJobRepository) AdvanceIDsScan(ctx context.Context, id bson.ObjectID, workerID string, collected int, now time.Time, lease time.Duration) error {
	res, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id, "claimedBy": workerID},
		bson.M{
			"$set": bson.M{"counts.succeeded": collected, "leaseExpiresAt": now.Add(lease)},
			"$inc": bson.M{"progress.batchesDone": 1},
		},
	)
	if err != nil {
		return apperror.Internal("advance ids scan", err)
	}
	if res.MatchedCount == 0 {
		return ErrLeaseLost
	}
	return nil
}

// SetResultKey records the rendered-PDF storage key for a qr job.
func (r *BulkJobRepository) SetResultKey(ctx context.Context, id bson.ObjectID, key string) error {
	_, err := r.coll.UpdateByID(ctx, id, bson.M{"$set": bson.M{"resultStorageKey": key}})
	if err != nil {
		return apperror.Internal("set bulk job result key", err)
	}
	return nil
}

// MarkTerminal finalizes a job: sets the terminal status + completedAt and
// clears the lease so it is never reclaimed. Guarded on claimedBy.
func (r *BulkJobRepository) MarkTerminal(ctx context.Context, id bson.ObjectID, workerID string, status models.BulkJobStatus, lastErr string, now time.Time) error {
	set := bson.M{"status": status, "completedAt": now}
	if lastErr != "" {
		set["lastError"] = lastErr
	}
	unset := bson.M{"claimedBy": "", "claimedAt": "", "leaseExpiresAt": ""}
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id, "claimedBy": workerID},
		bson.M{"$set": set, "$unset": unset},
	)
	if err != nil {
		return apperror.Internal("mark bulk job terminal", err)
	}
	return nil
}

// MarkNotified stamps that the completion digest step has run, so it is never
// repeated. Idempotent guard for the once-per-job digest guarantee.
func (r *BulkJobRepository) MarkNotified(ctx context.Context, id bson.ObjectID, now time.Time) (bool, error) {
	res, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id, "notifiedAt": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"notifiedAt": now}},
	)
	if err != nil {
		return false, apperror.Internal("mark bulk job notified", err)
	}
	return res.ModifiedCount > 0, nil
}

// FindExpiredResults returns completed qr/ids jobs whose artifact is older than
// `before` and still present, for the retention-cleanup cron.
func (r *BulkJobRepository) FindExpiredResults(ctx context.Context, before time.Time, limit int64) ([]BulkJobDoc, error) {
	filter := bson.M{
		"type":             bson.M{"$in": bson.A{models.BulkJobTypeQr, models.BulkJobTypeIds}},
		"resultStorageKey": bson.M{"$exists": true, "$ne": ""},
		"completedAt":      bson.M{"$lt": before},
	}
	opts := options.Find().SetLimit(limit)
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, apperror.Internal("find expired bulk results", err)
	}
	var docs []BulkJobDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, apperror.Internal("decode expired bulk results", err)
	}
	return docs, nil
}

// ClearResultKey unsets the storage key after its file is deleted by cleanup.
// A completed qr job with no key thereafter reads as "result expired" (410).
func (r *BulkJobRepository) ClearResultKey(ctx context.Context, id bson.ObjectID) error {
	_, err := r.coll.UpdateByID(ctx, id, bson.M{"$unset": bson.M{"resultStorageKey": ""}})
	if err != nil {
		return apperror.Internal("clear bulk job result key", err)
	}
	return nil
}
