package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/notification"
	"imp/internal/repository"
	"imp/internal/storage"
)

// BulkJobConfig carries the execution knobs (all sourced from env in main).
type BulkJobConfig struct {
	MaxAssets   int
	BatchSize   int
	MaxAttempts int           // per-batch txn retry cap before rows are errored out
	ErrorCap    int           // max row errors retained on a job doc
	Lease       time.Duration // worker lease duration
	ResultTTL   time.Duration // qr result retention

	IDsMaxLimit  int // ids export: request cap and default limit
	IDsBatchSize int // ids export: keyset batch size
}

// BulkJobService owns the async bulk-asset pipeline: synchronous enqueue-time
// validation (which preserves the pre-async 400/diagnostics contracts), and the
// worker-side batch executor that applies rows via the SAME shared apply*
// helpers the single-asset endpoints use. It reaches assets/movements/etc.
// through the embedded *AssetService (same package), so Movement shapes are
// identical between the single and bulk paths.
type BulkJobService struct {
	asset   *AssetService
	bulk    *repository.BulkJobRepository
	storage storage.FileStorage
	cfg     BulkJobConfig
	logger  *slog.Logger
}

func NewBulkJobService(asset *AssetService, bulk *repository.BulkJobRepository, fs storage.FileStorage, cfg BulkJobConfig, logger *slog.Logger) *BulkJobService {
	if cfg.MaxAssets == 0 {
		cfg.MaxAssets = 5000
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 100
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.ErrorCap == 0 {
		cfg.ErrorCap = 1000
	}
	if cfg.Lease == 0 {
		cfg.Lease = 120 * time.Second
	}
	if cfg.IDsMaxLimit == 0 {
		cfg.IDsMaxLimit = 100000
	}
	if cfg.IDsBatchSize == 0 {
		cfg.IDsBatchSize = 1000
	}
	return &BulkJobService{asset: asset, bulk: bulk, storage: fs, cfg: cfg, logger: logger}
}

// ---------------------------------------------------------------------------
// Enqueue helpers
// ---------------------------------------------------------------------------

// dedupeAndCap removes duplicate asset ids (preserving first-occurrence order)
// and enforces the [1, MaxAssets] bound. An out-of-range batch is a global 400.
func (s *BulkJobService) dedupeAndCap(ids []bson.ObjectID) ([]bson.ObjectID, error) {
	seen := make(map[bson.ObjectID]struct{}, len(ids))
	out := make([]bson.ObjectID, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, apperror.BadRequest("assetIds is required")
	}
	if len(out) > s.cfg.MaxAssets {
		return nil, apperror.BadRequest(fmt.Sprintf("batch exceeds BULK_MAX_ASSETS (%d)", s.cfg.MaxAssets))
	}
	return out, nil
}

// mapLookup bulk-loads the assets and returns a closure compatible with the
// existing bulk validators. Missing ids yield apperror.NotFound so the
// validators classify them as not_found.
func (s *BulkJobService) mapLookup(ctx context.Context, ids []bson.ObjectID) (func(bson.ObjectID) (*models.Asset, error), error) {
	m, err := s.asset.assets.LoadByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return func(id bson.ObjectID) (*models.Asset, error) {
		if a, ok := m[id]; ok {
			return a, nil
		}
		return nil, apperror.NotFound("asset not found")
	}, nil
}

func assetIDsOfTransfer(oks []validatedTransfer) []bson.ObjectID {
	out := make([]bson.ObjectID, 0, len(oks))
	for _, v := range oks {
		out = append(out, v.asset.ID)
	}
	return out
}

func assetIDsOfStatus(oks []validatedStatus) []bson.ObjectID {
	out := make([]bson.ObjectID, 0, len(oks))
	for _, v := range oks {
		out = append(out, v.asset.ID)
	}
	return out
}

// hasGlobalFailure reports whether the diagnostics contain a whole-batch
// (synthetic, NilObjectID) failure row — e.g. dest_venue_forbidden. Such a
// failure can never be salvaged by validOnly.
func hasGlobalFailure(results []models.BulkActionResult) bool {
	for _, r := range results {
		if !r.Ok && r.AssetID == bson.NilObjectID {
			return true
		}
	}
	return false
}

// rowErrorsFrom converts failed per-row diagnostics (excluding synthetic global
// rows) into job row errors.
func rowErrorsFrom(results []models.BulkActionResult) []models.BulkJobRowError {
	out := make([]models.BulkJobRowError, 0)
	for _, r := range results {
		if r.Ok || r.AssetID == bson.NilObjectID {
			continue
		}
		code := ""
		if r.Error != nil {
			code = *r.Error
		}
		out = append(out, models.BulkJobRowError{AssetID: r.AssetID, Code: code, Message: code})
	}
	return out
}

// newJobDoc assembles a queued BulkJobDoc with capped seed errors.
func (s *BulkJobService) newJobDoc(
	typ models.BulkJobType,
	requestedBy bson.ObjectID,
	processIDs []bson.ObjectID,
	attachmentIDs []bson.ObjectID,
	validOnly bool,
	params repository.BulkJobParams,
	total, seedFailed, seedSkipped int,
	seedErrors []models.BulkJobRowError,
) *repository.BulkJobDoc {
	batchesTotal := 0
	if s.cfg.BatchSize > 0 {
		batchesTotal = (len(processIDs) + s.cfg.BatchSize - 1) / s.cfg.BatchSize
	}
	errs, truncated := capErrors(seedErrors, s.cfg.ErrorCap)
	return &repository.BulkJobDoc{
		BulkJob: models.BulkJob{
			Type:   typ,
			Status: models.BulkJobStatusQueued,
			Counts: models.BulkJobCounts{
				Total:   total,
				Failed:  seedFailed,
				Skipped: seedSkipped,
			},
			Progress:        models.BulkJobProgress{BatchesTotal: batchesTotal},
			Errors:          errs,
			ErrorsTruncated: truncated,
			RequestedBy:     requestedBy,
		},
		Params:        params,
		AssetIDs:      processIDs,
		AttachmentIDs: attachmentIDs,
		ValidOnly:     validOnly,
		BatchSize:     s.cfg.BatchSize,
		Cursor:        0,
	}
}

// capErrors trims a row-error slice to cap, returning the (possibly trimmed)
// slice and whether truncation occurred. A non-nil empty slice is returned so
// the API's required errors[] is never null.
func capErrors(errs []models.BulkJobRowError, cap int) ([]models.BulkJobRowError, bool) {
	if errs == nil {
		errs = []models.BulkJobRowError{}
	}
	if cap > 0 && len(errs) > cap {
		return errs[:cap], true
	}
	return errs, false
}

// persist creates the job doc and reserves its attachments against the orphan
// sweep. A reservation failure is logged but does not fail enqueue — the
// attachments were validated moments ago and won't be swept for 24h.
func (s *BulkJobService) persist(ctx context.Context, job *repository.BulkJobDoc, attachmentIDs []bson.ObjectID) error {
	if err := s.bulk.Create(ctx, job); err != nil {
		return err
	}
	if err := s.asset.attachments.ReserveForJob(ctx, attachmentIDs, job.ID); err != nil {
		s.logger.Error("bulk_job_reserve_attachments_failed", slog.String("job", job.ID.Hex()), slog.Any("err", err))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Enqueue: one function per endpoint. Each returns exactly one of:
//   (*BulkJob, nil, nil)          → 202
//   (nil, *diagnostics, nil)      → 200 today's per-row diagnostics (strict fail)
//   (nil, nil, err)               → 400 / attachment-validation error
// ---------------------------------------------------------------------------

func (s *BulkJobService) EnqueueTransfer(ctx context.Context, performedBy bson.ObjectID, p Principal, in models.BulkTransferRequest) (*models.BulkJob, *models.BulkActionResponse, error) {
	ids, err := s.dedupeAndCap(in.AssetIDs)
	if err != nil {
		return nil, nil, err
	}
	in.AssetIDs = ids

	attachmentIDs := derefAttachmentIDs(in.AttachmentIDs)
	if err := s.asset.validateAttachments(ctx, attachmentIDs, performedBy); err != nil {
		return nil, nil, err
	}

	destExists := true
	if _, err := s.asset.venues.FindByID(ctx, in.ToVenueID); err != nil {
		if appErr, ok := apperror.As(err); !ok || appErr.Kind != apperror.KindNotFound {
			return nil, nil, err
		}
		destExists = false
	}

	lookup, err := s.mapLookup(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	oks, results, allOK := validateBulkTransferRequest(in, p, lookup, destExists, s.cfg.MaxAssets)

	validOnly := in.ValidOnly != nil && *in.ValidOnly
	if !allOK && (!validOnly || hasGlobalFailure(results)) {
		return nil, s.asset.bulkResponse(results), nil
	}

	processIDs := assetIDsOfTransfer(oks)
	rowErrs := rowErrorsFrom(results)
	params := repository.BulkJobParams{
		ToVenueID:          &in.ToVenueID,
		ExpectedReturnDate: in.ExpectedReturnDate,
		Notes:              in.Notes,
	}
	job := s.newJobDoc(models.BulkJobTypeTransfer, performedBy, processIDs, attachmentIDs, validOnly, params, len(ids), len(rowErrs), 0, rowErrs)
	if err := s.persist(ctx, job, attachmentIDs); err != nil {
		return nil, nil, err
	}
	return &job.BulkJob, nil, nil
}

func (s *BulkJobService) EnqueueStatus(ctx context.Context, performedBy bson.ObjectID, p Principal, in models.BulkStatusRequest) (*models.BulkJob, *models.BulkActionResponse, error) {
	ids, err := s.dedupeAndCap(in.AssetIDs)
	if err != nil {
		return nil, nil, err
	}
	in.AssetIDs = ids

	attachmentIDs := derefAttachmentIDs(in.AttachmentIDs)
	if err := s.asset.validateAttachments(ctx, attachmentIDs, performedBy); err != nil {
		return nil, nil, err
	}

	lookup, err := s.mapLookup(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	oks, results, allOK := validateBulkStatusRequest(in, p, lookup, s.cfg.MaxAssets)

	validOnly := in.ValidOnly != nil && *in.ValidOnly
	if !allOK && (!validOnly || hasGlobalFailure(results)) {
		return nil, s.asset.bulkResponse(results), nil
	}

	processIDs := assetIDsOfStatus(oks)
	rowErrs := rowErrorsFrom(results)
	params := repository.BulkJobParams{Status: &in.Status, Reason: in.Reason}
	job := s.newJobDoc(models.BulkJobTypeStatus, performedBy, processIDs, attachmentIDs, validOnly, params, len(ids), len(rowErrs), 0, rowErrs)
	if err := s.persist(ctx, job, attachmentIDs); err != nil {
		return nil, nil, err
	}
	return &job.BulkJob, nil, nil
}

func (s *BulkJobService) EnqueueAssign(ctx context.Context, performedBy bson.ObjectID, p Principal, in models.BulkAssignRequest) (*models.BulkJob, *models.BulkAssignResponse, error) {
	ids, err := s.dedupeAndCap(in.AssetIDs)
	if err != nil {
		return nil, nil, err
	}
	in.AssetIDs = ids

	// Unknown/inactive target user is always a whole-request 400.
	u, err := s.asset.users.FindByID(ctx, in.ResponsibleUserID)
	if err != nil {
		if isKind(err, apperror.KindNotFound) {
			return nil, nil, apperror.BadRequest("responsibleUserId does not resolve to a known user")
		}
		return nil, nil, err
	}
	if !u.IsActive {
		return nil, nil, apperror.BadRequest("responsibleUserId is not an active user")
	}

	lookup, err := s.mapLookup(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	oks, results, allOK := validateBulkAssignRequest(in, p, lookup, s.cfg.MaxAssets)

	validOnly := in.ValidOnly != nil && *in.ValidOnly
	if !allOK && (!validOnly || hasGlobalFailure(results)) {
		return nil, bulkAssignResponse(nil, results), nil
	}

	// Enqueue only the rows that need a write; pre-count already-assigned rows
	// as skipped. In-batch re-read handles the TOCTOU case (a non-no-op row
	// that becomes a no-op by execution time is skipped there too).
	processIDs := make([]bson.ObjectID, 0, len(oks))
	skipped := 0
	for _, v := range oks {
		if v.noOp {
			skipped++
			continue
		}
		processIDs = append(processIDs, v.asset.ID)
	}

	attachmentIDs := derefAttachmentIDs(in.AttachmentIDs)
	// Validate attachments only when we'll actually link them (mirrors the
	// sync path: an all-no-op batch must not consume/orphan attachments).
	if len(processIDs) > 0 {
		if err := s.asset.validateAttachments(ctx, attachmentIDs, performedBy); err != nil {
			return nil, nil, err
		}
	} else {
		attachmentIDs = nil
	}

	rowErrs := rowErrorsFrom(results)
	params := repository.BulkJobParams{ResponsibleUserID: &in.ResponsibleUserID, Notes: in.Notes}
	job := s.newJobDoc(models.BulkJobTypeAssign, performedBy, processIDs, attachmentIDs, validOnly, params, len(ids), len(rowErrs), skipped, rowErrs)
	if err := s.persist(ctx, job, attachmentIDs); err != nil {
		return nil, nil, err
	}
	return &job.BulkJob, nil, nil
}

func (s *BulkJobService) EnqueueCondition(ctx context.Context, performedBy bson.ObjectID, p Principal, in models.BulkConditionUpdate) (*models.BulkJob, error) {
	ids, err := s.dedupeAndCap(in.AssetIDs)
	if err != nil {
		return nil, err
	}
	in.AssetIDs = ids

	attachmentIDs := derefAttachmentIDs(in.AttachmentIDs)
	if err := s.asset.validateAttachments(ctx, attachmentIDs, performedBy); err != nil {
		return nil, err
	}

	lookup, err := s.mapLookup(ctx, ids)
	if err != nil {
		return nil, err
	}
	// Best-effort: per-asset RBAC + not_found + unchanged become pre-counted
	// skips (never a 400). Only a malformed request (bad enum/empty/over-cap)
	// is a global 400.
	toUpdate, skipped, err := classifyBulkCondition(in, p, lookup, s.cfg.MaxAssets)
	if err != nil {
		return nil, err
	}

	params := repository.BulkJobParams{Condition: &in.Condition, Notes: in.Notes}
	job := s.newJobDoc(models.BulkJobTypeCondition, performedBy, toUpdate, attachmentIDs, false, params, len(ids), 0, len(skipped), nil)
	if err := s.persist(ctx, job, attachmentIDs); err != nil {
		return nil, err
	}
	return &job.BulkJob, nil
}

func (s *BulkJobService) EnqueueQR(ctx context.Context, performedBy bson.ObjectID, p Principal, in models.BulkQrRequest) (*models.BulkJob, error) {
	ids, err := s.dedupeAndCap(in.AssetIDs)
	if err != nil {
		return nil, err
	}
	// Existence + per-asset RBAC, evaluated now (hard 400/403/404 on failure).
	if _, err := s.asset.QRBulkValidate(ctx, p, ids); err != nil {
		return nil, err
	}
	job := s.newJobDoc(models.BulkJobTypeQr, performedBy, ids, nil, false, repository.BulkJobParams{}, len(ids), 0, 0, nil)
	// qr renders a single artifact, not per-batch; batchesTotal is meaningless.
	job.Progress.BatchesTotal = 1
	if err := s.persist(ctx, job, nil); err != nil {
		return nil, err
	}
	return &job.BulkJob, nil
}

// EnqueueIDs validates an asset-ids export request synchronously (400 on
// malformed filter ObjectIds — same messages as GET /assets — or an
// out-of-range limit) and persists a queued ids job. The caller's venue scope
// is resolved and stored NOW (authorization evaluated once, at enqueue), so the
// worker can only ever collect ids the caller could see in the list. Returns
// 202 + the BulkJob.
func (s *BulkJobService) EnqueueIDs(ctx context.Context, requestedBy bson.ObjectID, p Principal, in models.BulkIdsRequest) (*models.BulkJob, error) {
	q, err := BuildAssetListQuery(in.Filters)
	if err != nil {
		return nil, err
	}

	limit := s.cfg.IDsMaxLimit
	if in.Limit != nil {
		limit = *in.Limit
	}
	if limit < 1 || limit > s.cfg.IDsMaxLimit {
		return nil, apperror.BadRequest(fmt.Sprintf("limit must be between 1 and ASSET_IDS_MAX_LIMIT (%d)", s.cfg.IDsMaxLimit))
	}

	params, err := idsParamsFrom(q, p, limit)
	if err != nil {
		return nil, err
	}

	job := &repository.BulkJobDoc{
		BulkJob: models.BulkJob{
			Type:            models.BulkJobTypeIds,
			Status:          models.BulkJobStatusQueued,
			Counts:          models.BulkJobCounts{}, // total unknown until the scan counts it
			Progress:        models.BulkJobProgress{},
			Errors:          []models.BulkJobRowError{}, // API requires non-null errors[]
			ErrorsTruncated: false,
			RequestedBy:     requestedBy,
		},
		Params:    repository.BulkJobParams{IDs: params},
		BatchSize: s.cfg.IDsBatchSize,
	}
	if err := s.persist(ctx, job, nil); err != nil {
		return nil, err
	}
	return &job.BulkJob, nil
}

// idsParamsFrom converts a parsed AssetListQuery + principal into the durable
// IDsParams, resolving venue scope exactly as the GET /assets handler does:
// admins are unrestricted (Scoped=false); non-admins are pinned to their JWT
// venue ids (Scoped=true, empty allowed → matches nothing).
func idsParamsFrom(q AssetListQuery, p Principal, limit int) (*repository.IDsParams, error) {
	ip := &repository.IDsParams{
		Venue:        q.Venue,
		CurrentVenue: q.CurrentVenue,
		Category:     q.Category,
		Department:   q.Department,
		Responsible:  q.Responsible,
		Away:         q.Away,
		Overdue:      q.Overdue,
		Q:            q.Q,
		Limit:        limit,
	}
	if q.Status != "" {
		st := q.Status
		ip.Status = &st
	}
	if !p.IsAdmin {
		ip.Scoped = true
		ids := make([]bson.ObjectID, 0, len(p.VenueIDs))
		for hex := range p.VenueIDs {
			id, err := bson.ObjectIDFromHex(hex)
			if err != nil {
				return nil, apperror.BadRequest("invalid venue scope")
			}
			ids = append(ids, id)
		}
		ip.ScopeVenueIDs = ids
	}
	return ip, nil
}

// idsQueryFrom rebuilds the AssetListQuery (including venue scope) from
// persisted params for the worker. Preserves the nil-vs-empty scope
// distinction: a scoped job with zero venues yields a non-nil empty Scope
// (matches nothing), while an unscoped (admin) job leaves Scope nil
// (unrestricted).
func idsQueryFrom(ip *repository.IDsParams) AssetListQuery {
	q := AssetListQuery{
		Venue:        ip.Venue,
		CurrentVenue: ip.CurrentVenue,
		Category:     ip.Category,
		Department:   ip.Department,
		Responsible:  ip.Responsible,
		Away:         ip.Away,
		Overdue:      ip.Overdue,
		Q:            ip.Q,
	}
	if ip.Status != nil {
		q.Status = *ip.Status
	}
	if ip.Scoped {
		scope := ip.ScopeVenueIDs
		if scope == nil {
			scope = []bson.ObjectID{}
		}
		q.Scope = scope
	}
	return q
}

// ---------------------------------------------------------------------------
// Read paths
// ---------------------------------------------------------------------------

func (s *BulkJobService) Get(ctx context.Context, id bson.ObjectID) (*repository.BulkJobDoc, error) {
	return s.bulk.FindByID(ctx, id)
}

// CleanupExpiredResults deletes the rendered PDFs of qr jobs whose retention
// window (ResultTTL) has elapsed and clears their storage key, so the /result
// endpoint returns 410 afterwards. Returns the number of results cleaned. Run
// from the daily cron. The job doc itself is retained for status queries.
func (s *BulkJobService) CleanupExpiredResults(ctx context.Context) (int, error) {
	if s.cfg.ResultTTL <= 0 {
		return 0, nil
	}
	before := time.Now().UTC().Add(-s.cfg.ResultTTL)
	docs, err := s.bulk.FindExpiredResults(ctx, before, 500)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for i := range docs {
		d := &docs[i]
		if err := s.storage.Delete(ctx, d.ResultStorageKey); err != nil {
			s.logger.Error("bulk_result_delete_bytes_failed", slog.String("job", d.ID.Hex()), slog.Any("err", err))
			// Still clear the key so we don't retry forever; the sweep will
			// eventually reconcile any leaked file.
		}
		if err := s.bulk.ClearResultKey(ctx, d.ID); err != nil {
			s.logger.Error("bulk_result_clear_key_failed", slog.String("job", d.ID.Hex()), slog.Any("err", err))
			continue
		}
		cleaned++
	}
	return cleaned, nil
}

// OpenResult opens the stored QR PDF for a completed qr job. The caller is
// responsible for RBAC + readiness checks (see handler). Returns a ReadCloser
// the caller must close.
func (s *BulkJobService) OpenResult(ctx context.Context, job *repository.BulkJobDoc) (io.ReadCloser, error) {
	if job.ResultStorageKey == "" {
		return nil, apperror.Gone("job result has expired")
	}
	rc, err := s.storage.Get(ctx, job.ResultStorageKey)
	if err != nil {
		return nil, apperror.Internal("open job result", err)
	}
	return rc, nil
}

func (s *BulkJobService) List(ctx context.Context, status *models.BulkJobStatus, typ *models.BulkJobType, limit int64) ([]models.BulkJob, error) {
	docs, err := s.bulk.List(ctx, status, typ, limit)
	if err != nil {
		return nil, err
	}
	out := make([]models.BulkJob, 0, len(docs))
	for i := range docs {
		out = append(out, docs[i].BulkJob)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Worker-side execution
// ---------------------------------------------------------------------------

// ClaimAndRunOnce atomically claims the oldest runnable job and executes it to
// a terminal state. Returns claimed=false when nothing was available. Safe to
// call from multiple workers/instances concurrently — the claim is atomic and
// the cursor advances only inside committed batch txns.
func (s *BulkJobService) ClaimAndRunOnce(ctx context.Context, workerID string) (claimed bool, err error) {
	job, err := s.bulk.Claim(ctx, workerID, time.Now().UTC(), s.cfg.Lease)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	if rerr := s.runJob(ctx, job, workerID); rerr != nil {
		return true, rerr
	}
	return true, nil
}

func (s *BulkJobService) runJob(ctx context.Context, job *repository.BulkJobDoc, workerID string) error {
	if job.Type == models.BulkJobTypeQr {
		return s.runQRJob(ctx, job, workerID)
	}
	if job.Type == models.BulkJobTypeIds {
		return s.runIDsJob(ctx, job, workerID)
	}

	cursor := job.Cursor
	for cursor < len(job.AssetIDs) {
		end := min(cursor+job.BatchSize, len(job.AssetIDs))
		batch := job.AssetIDs[cursor:end]

		done, err := s.processBatchWithRetry(ctx, job, workerID, cursor, batch)
		if err != nil {
			if errors.Is(err, repository.ErrLeaseLost) {
				// Another worker reclaimed this job; yield without finalizing.
				s.logger.Warn("bulk_job_lease_lost", slog.String("job", job.ID.Hex()), slog.String("worker", workerID))
				return nil
			}
			// Fatal: could neither commit the batch nor durably record its
			// failure. Mark the job failed so it doesn't spin forever.
			s.logger.Error("bulk_job_batch_fatal", slog.String("job", job.ID.Hex()), slog.Any("err", err))
			_ = s.bulk.MarkTerminal(ctx, job.ID, workerID, models.BulkJobStatusFailed, err.Error(), time.Now().UTC())
			_ = s.asset.attachments.ReleaseJobReservation(ctx, job.ID)
			return err
		}
		cursor = end
		if !done {
			// Lost lease mid-way (recorded as a soft stop); yield.
			return nil
		}
		if err := s.bulk.ExtendLease(ctx, job.ID, workerID, time.Now().UTC(), s.cfg.Lease); err != nil {
			s.logger.Warn("bulk_job_extend_lease_failed", slog.String("job", job.ID.Hex()), slog.Any("err", err))
		}
	}

	return s.finalize(ctx, job, workerID)
}

// processBatchWithRetry runs one batch, retrying transient txn failures up to
// MaxAttempts. After the cap it records the batch's rows as errors and advances
// the cursor (non-transactionally) so the job never wedges. Returns done=false
// only when the lease was lost.
func (s *BulkJobService) processBatchWithRetry(ctx context.Context, job *repository.BulkJobDoc, workerID string, startCursor int, batch []bson.ObjectID) (done bool, err error) {
	backoff := 50 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= s.cfg.MaxAttempts; attempt++ {
		perr := s.processBatch(ctx, job, workerID, startCursor, batch)
		if perr == nil {
			return true, nil
		}
		if errors.Is(perr, repository.ErrLeaseLost) {
			return false, repository.ErrLeaseLost
		}
		lastErr = perr
		s.logger.Warn("bulk_job_batch_retry",
			slog.String("job", job.ID.Hex()),
			slog.Int("attempt", attempt),
			slog.Any("err", perr))
		if attempt < s.cfg.MaxAttempts {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
	}

	// Cap exhausted: record every row in this batch as an error and advance
	// past it (no asset writes were committed since the txn kept failing).
	rowErrs := make([]models.BulkJobRowError, 0, len(batch))
	for _, id := range batch {
		rowErrs = append(rowErrs, models.BulkJobRowError{AssetID: id, Code: "batch_failed", Message: lastErr.Error()})
	}
	upd := repository.BatchUpdate{
		NewCursor: startCursor + len(batch),
		FailedInc: len(batch),
	}
	s.attachCappedErrors(ctx, job, &upd, rowErrs)
	if aerr := s.bulk.ApplyBatchResult(ctx, job.ID, workerID, startCursor, upd); aerr != nil {
		if errors.Is(aerr, repository.ErrLeaseLost) {
			return false, repository.ErrLeaseLost
		}
		return false, aerr
	}
	return true, nil
}

// processBatch executes a single batch in one Mongo transaction: reload the
// batch's assets, apply each row via the shared apply* helper (stamping
// bulkJobId on every Movement), link attachments for succeeded rows, and
// advance the cursor + counts on the job doc conditionally (ErrLeaseLost aborts
// the whole txn so nothing commits).
func (s *BulkJobService) processBatch(ctx context.Context, job *repository.BulkJobDoc, workerID string, startCursor int, batch []bson.ObjectID) error {
	sess, err := s.asset.client.StartSession()
	if err != nil {
		return apperror.Internal("start session", err)
	}
	defer sess.EndSession(ctx)

	_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		sc = withBulkJobID(sc, job.ID)

		assetMap, err := s.asset.assets.LoadByIDs(sc, batch)
		if err != nil {
			return nil, err
		}

		upd := repository.BatchUpdate{NewCursor: startCursor + len(batch)}
		var rowErrs []models.BulkJobRowError
		for _, id := range batch {
			outcome, rowErr, infraErr := s.applyRow(sc, job, assetMap[id], id)
			if infraErr != nil {
				return nil, infraErr // abort whole batch → retry
			}
			switch outcome {
			case outcomeSucceeded:
				upd.SucceededInc++
			case outcomeSkipped:
				upd.SkippedInc++
			case outcomeErrored:
				upd.FailedInc++
				if rowErr != nil {
					rowErrs = append(rowErrs, *rowErr)
				}
			}
		}
		s.attachCappedErrors(sc, job, &upd, rowErrs)

		if err := s.bulk.ApplyBatchResult(sc, job.ID, workerID, startCursor, upd); err != nil {
			return nil, err // ErrLeaseLost or infra → abort txn (no writes commit)
		}
		return nil, nil
	})
	return err
}

type rowOutcome int

const (
	outcomeSucceeded rowOutcome = iota
	outcomeSkipped
	outcomeErrored
)

// applyRow applies one asset for the job's action, returning the outcome and,
// for errors, the row error to record. A non-nil third return is an
// infrastructure error that must abort (and retry) the whole batch.
func (s *BulkJobService) applyRow(ctx context.Context, job *repository.BulkJobDoc, a *models.Asset, id bson.ObjectID) (rowOutcome, *models.BulkJobRowError, error) {
	performedBy := job.RequestedBy
	attachmentIDs := job.AttachmentIDs

	switch job.Type {
	case models.BulkJobTypeTransfer:
		if a == nil {
			return outcomeErrored, rowErr(id, "not_found", "asset not found at execution"), nil
		}
		_, err := s.asset.applyTransfer(ctx, a, performedBy, models.TransferAssetRequest{
			ToVenueID:          *job.Params.ToVenueID,
			ExpectedReturnDate: job.Params.ExpectedReturnDate,
			Notes:              job.Params.Notes,
		}, attachmentIDs)
		return s.classifyApplyErr(id, err, "same_venue")

	case models.BulkJobTypeStatus:
		if a == nil {
			return outcomeErrored, rowErr(id, "not_found", "asset not found at execution"), nil
		}
		_, err := s.asset.applyStatusChange(ctx, a, performedBy, models.StatusChangeRequest{
			Status: *job.Params.Status,
			Reason: job.Params.Reason,
		}, attachmentIDs)
		return s.classifyApplyErr(id, err, "invalid_transition")

	case models.BulkJobTypeAssign:
		if a == nil {
			return outcomeErrored, rowErr(id, "not_found", "asset not found at execution"), nil
		}
		// Re-read custodian: still assigned → skip, NO movement.
		if a.ResponsibleUserID != nil && *a.ResponsibleUserID == *job.Params.ResponsibleUserID {
			return outcomeSkipped, nil, nil
		}
		_, err := s.asset.applyAssignCustody(ctx, a, performedBy, *job.Params.ResponsibleUserID, job.Params.Notes, attachmentIDs)
		if err != nil {
			return s.classifyApplyErr(id, err, "conflict")
		}
		return outcomeSucceeded, nil, nil

	case models.BulkJobTypeCondition:
		// Best-effort: not_found / unchanged → skip (never an error).
		if a == nil {
			return outcomeSkipped, nil, nil
		}
		if err := validateConditionChange(a.Condition, *job.Params.Condition); err != nil {
			if isKind(err, apperror.KindConflict) {
				return outcomeSkipped, nil, nil // unchanged
			}
			return outcomeErrored, rowErr(id, "invalid_condition", err.Error()), nil
		}
		if _, err := s.asset.applyConditionUpdate(ctx, a, performedBy, models.ConditionUpdate{
			Condition: *job.Params.Condition,
			Notes:     job.Params.Notes,
		}, attachmentIDs); err != nil {
			if isKind(err, apperror.KindConflict) {
				return outcomeSkipped, nil, nil
			}
			return outcomeErrored, nil, err // infra
		}
		return outcomeSucceeded, nil, nil
	}
	return outcomeErrored, rowErr(id, "unknown_type", "unknown job type"), nil
}

// classifyApplyErr maps an apply* helper's error to a row outcome: a business
// Conflict is a TOCTOU row error (using conflictCode); anything else is an
// infrastructure error that aborts the batch.
func (s *BulkJobService) classifyApplyErr(id bson.ObjectID, err error, conflictCode string) (rowOutcome, *models.BulkJobRowError, error) {
	if err == nil {
		return outcomeSucceeded, nil, nil
	}
	if isKind(err, apperror.KindConflict) {
		return outcomeErrored, rowErr(id, conflictCode, err.Error()), nil
	}
	if isKind(err, apperror.KindNotFound) {
		return outcomeErrored, rowErr(id, "not_found", err.Error()), nil
	}
	return outcomeErrored, nil, err // infra → abort + retry
}

func rowErr(id bson.ObjectID, code, msg string) *models.BulkJobRowError {
	return &models.BulkJobRowError{AssetID: id, Code: code, Message: msg}
}

// attachCappedErrors appends as many new row errors to the batch update as the
// per-job error cap allows, setting the truncated flag when the cap is hit. It
// reads the job's current error count via the doc it last saw plus this batch's
// running increments — the cap is a soft bound on retained detail, not on the
// failed counter (which always reflects the true total).
func (s *BulkJobService) attachCappedErrors(ctx context.Context, job *repository.BulkJobDoc, upd *repository.BatchUpdate, newErrs []models.BulkJobRowError) {
	_ = ctx
	if len(newErrs) == 0 {
		return
	}
	// Best-effort current retained count: re-fetch is avoided for speed; use
	// len(job.Errors) as a lower bound and rely on the truncated flag being
	// monotonic. To stay correct under many batches, fetch the live count.
	current := len(job.Errors)
	if live, err := s.bulk.FindByID(ctx, job.ID); err == nil {
		current = len(live.Errors)
	}
	room := s.cfg.ErrorCap - current
	if room <= 0 {
		upd.SetErrorsTruncated = true
		return
	}
	if len(newErrs) > room {
		upd.NewErrors = newErrs[:room]
		upd.SetErrorsTruncated = true
		return
	}
	upd.NewErrors = newErrs
}

// ---------------------------------------------------------------------------
// Finalization + completion digests
// ---------------------------------------------------------------------------

func (s *BulkJobService) finalize(ctx context.Context, job *repository.BulkJobDoc, workerID string) error {
	final, err := s.bulk.FindByID(ctx, job.ID)
	if err != nil {
		return err
	}

	// Completion digests, exactly once per job (MarkNotified guard).
	if final.Type == models.BulkJobTypeTransfer || final.Type == models.BulkJobTypeAssign {
		claimed, nerr := s.bulk.MarkNotified(ctx, final.ID, time.Now().UTC())
		if nerr != nil {
			return nerr
		}
		if claimed {
			s.enqueueCompletionDigest(ctx, final)
		}
	}

	status := terminalStatus(final.Counts)
	if err := s.bulk.MarkTerminal(ctx, final.ID, workerID, status, "", time.Now().UTC()); err != nil {
		return err
	}
	// Release the attachment reservation now the job is terminal. Linked
	// attachments stay linked; a fully-failed job's attachments become
	// sweepable again.
	if err := s.asset.attachments.ReleaseJobReservation(ctx, final.ID); err != nil {
		s.logger.Warn("bulk_job_release_reservation_failed", slog.String("job", final.ID.Hex()), slog.Any("err", err))
	}
	return nil
}

// terminalStatus mirrors the import pipeline: failed only when nothing
// succeeded AND nothing was a legitimate skip; completed_with_errors when there
// were row errors; completed otherwise.
func terminalStatus(c models.BulkJobCounts) models.BulkJobStatus {
	if c.Succeeded == 0 && c.Skipped == 0 {
		return models.BulkJobStatusFailed
	}
	if c.Failed > 0 {
		return models.BulkJobStatusCompletedWithErrors
	}
	return models.BulkJobStatusCompleted
}

// enqueueCompletionDigest aggregates the assets that actually moved / were
// assigned by querying Movements stamped with this job's id (append-only,
// crash-safe) and enqueues exactly one digest per recipient.
func (s *BulkJobService) enqueueCompletionDigest(ctx context.Context, job *repository.BulkJobDoc) {
	moves, err := s.asset.movements.ListByBulkJob(ctx, job.ID)
	if err != nil {
		s.logger.Error("bulk_job_digest_movements_failed", slog.String("job", job.ID.Hex()), slog.Any("err", err))
		return
	}
	if len(moves) == 0 {
		return
	}

	// Distinct affected asset ids, first-occurrence order.
	seen := make(map[bson.ObjectID]struct{}, len(moves))
	ids := make([]bson.ObjectID, 0, len(moves))
	for _, m := range moves {
		if _, ok := seen[m.AssetID]; ok {
			continue
		}
		seen[m.AssetID] = struct{}{}
		ids = append(ids, m.AssetID)
	}
	assetMap, err := s.asset.assets.LoadByIDs(ctx, ids)
	if err != nil {
		s.logger.Error("bulk_job_digest_assets_failed", slog.String("job", job.ID.Hex()), slog.Any("err", err))
		return
	}

	switch job.Type {
	case models.BulkJobTypeTransfer:
		// Group by home venue → one outbox row per home-venue manager.
		pos := map[bson.ObjectID]int{}
		groups := []notification.BulkTransferGroup{}
		for _, id := range ids {
			a := assetMap[id]
			if a == nil {
				continue
			}
			ref := notification.BulkTransferAssetRef{AssetID: a.ID, Tag: a.AssetTag, Name: a.Name, QRToken: a.QrToken}
			if i, ok := pos[a.HomeVenueID]; ok {
				groups[i].Assets = append(groups[i].Assets, ref)
				continue
			}
			pos[a.HomeVenueID] = len(groups)
			groups = append(groups, notification.BulkTransferGroup{HomeVenueID: a.HomeVenueID, Assets: []notification.BulkTransferAssetRef{ref}})
		}
		toVenue := bson.NilObjectID
		if job.Params.ToVenueID != nil {
			toVenue = *job.Params.ToVenueID
		}
		s.asset.triggers.BulkTransferDigest(ctx, groups, toVenue)

	case models.BulkJobTypeAssign:
		refs := make([]notification.BulkCustodyAssignedRef, 0, len(ids))
		for _, id := range ids {
			a := assetMap[id]
			if a == nil {
				continue
			}
			refs = append(refs, notification.BulkCustodyAssignedRef{
				AssetID:   a.ID,
				Tag:       a.AssetTag,
				Name:      a.Name,
				VenueName: venueLabelFor(ctx, s.asset.venues, a.CurrentVenueID),
				QRToken:   a.QrToken,
			})
		}
		if job.Params.ResponsibleUserID != nil {
			s.asset.triggers.BulkCustodyAssignedDigest(ctx, *job.Params.ResponsibleUserID, refs)
		}
	}
}

// ---------------------------------------------------------------------------
// QR job
// ---------------------------------------------------------------------------

func (s *BulkJobService) runQRJob(ctx context.Context, job *repository.BulkJobDoc, workerID string) error {
	pdf, err := s.asset.RenderQRForIDs(ctx, job.AssetIDs)
	if err != nil {
		s.logger.Error("bulk_job_qr_render_failed", slog.String("job", job.ID.Hex()), slog.Any("err", err))
		_ = s.bulk.MarkTerminal(ctx, job.ID, workerID, models.BulkJobStatusFailed, err.Error(), time.Now().UTC())
		return err
	}
	key, err := storage.NewKeyWithPrefix("bulk-jobs")
	if err != nil {
		return apperror.Internal("qr result key", err)
	}
	if err := s.storage.Put(ctx, key, bytes.NewReader(pdf), "application/pdf", int64(len(pdf))); err != nil {
		return apperror.Internal("store qr result", err)
	}
	if err := s.bulk.SetResultKey(ctx, job.ID, key); err != nil {
		return err
	}
	// One "batch" done for progress display.
	_ = s.bulk.ApplyBatchResult(ctx, job.ID, workerID, 0, repository.BatchUpdate{
		NewCursor:    len(job.AssetIDs),
		SucceededInc: len(job.AssetIDs),
	})
	if err := s.bulk.MarkTerminal(ctx, job.ID, workerID, models.BulkJobStatusCompleted, "", time.Now().UTC()); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// IDs export job
// ---------------------------------------------------------------------------

// runIDsJob collects asset ids matching the persisted GET /assets filter set
// (including venue scope) via keyset-paginated batches, DESCENDING by
// (createdAt, _id) — newest first, following the GET /assets list sort — up to
// the job's limit; when more match than limit, the newest `limit` are kept and
// the oldest are dropped. It writes a
// JSON artifact via FileStorage. Read-only: no transactions, no
// notifications, no per-row errors. On lease-expiry reclaim the scan restarts
// from zero (InitIDsScan resets counters) — cheap because it is read-only. Like
// runQRJob it fails fast on an infrastructure error (no whole-job retry).
func (s *BulkJobService) runIDsJob(ctx context.Context, job *repository.BulkJobDoc, workerID string) error {
	ip := job.Params.IDs
	if ip == nil {
		err := apperror.Internal("ids job missing params", nil)
		_ = s.bulk.MarkTerminal(ctx, job.ID, workerID, models.BulkJobStatusFailed, err.Error(), time.Now().UTC())
		return err
	}

	filter := buildAssetFilter(idsQueryFrom(ip))
	limit := ip.Limit
	batchSize := job.BatchSize
	if batchSize <= 0 {
		batchSize = s.cfg.IDsBatchSize
	}

	// Cheap capped count: limit+1 tells us matched (capped) and truncated.
	cnt, err := s.asset.assets.CountUpTo(ctx, filter, int64(limit)+1)
	if err != nil {
		return s.failIDsJob(ctx, job, workerID, err)
	}
	truncated := cnt > int64(limit)
	total := min(int(cnt), limit)
	batchesTotal := 0
	if batchSize > 0 {
		batchesTotal = (total + batchSize - 1) / batchSize
	}

	if err := s.bulk.InitIDsScan(ctx, job.ID, workerID, total, batchesTotal, time.Now().UTC(), s.cfg.Lease); err != nil {
		if errors.Is(err, repository.ErrLeaseLost) {
			s.logger.Warn("bulk_job_lease_lost", slog.String("job", job.ID.Hex()), slog.String("worker", workerID))
			return nil
		}
		return s.failIDsJob(ctx, job, workerID, err)
	}

	ids := make([]bson.ObjectID, 0, total)
	var cursor *repository.AssetKeysetCursor
	for len(ids) < limit {
		batch, err := s.asset.assets.FindIDsBefore(ctx, filter, cursor, batchSize)
		if err != nil {
			return s.failIDsJob(ctx, job, workerID, err)
		}
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			if len(ids) >= limit {
				break
			}
			ids = append(ids, row.ID)
			c := row
			cursor = &c
		}
		// Heartbeat + progress; also the ownership gate before the next batch and
		// before the artifact write. If the lease was lost, yield without writing.
		if err := s.bulk.AdvanceIDsScan(ctx, job.ID, workerID, len(ids), time.Now().UTC(), s.cfg.Lease); err != nil {
			if errors.Is(err, repository.ErrLeaseLost) {
				s.logger.Warn("bulk_job_lease_lost", slog.String("job", job.ID.Hex()), slog.String("worker", workerID))
				return nil
			}
			return s.failIDsJob(ctx, job, workerID, err)
		}
		if len(batch) < batchSize {
			break // last page
		}
	}

	// Write the artifact once, at completion. Zero matches still writes an empty
	// array so /result is deterministic.
	res := models.AssetIdsResult{
		JobID:       job.ID,
		GeneratedAt: time.Now().UTC(),
		Count:       len(ids),
		Truncated:   truncated,
		AssetIDs:    ids,
	}
	if res.AssetIDs == nil {
		res.AssetIDs = []bson.ObjectID{}
	}
	blob, err := json.Marshal(res)
	if err != nil {
		return s.failIDsJob(ctx, job, workerID, apperror.Internal("marshal ids result", err))
	}
	key, err := storage.NewKeyWithPrefix("bulk-jobs")
	if err != nil {
		return s.failIDsJob(ctx, job, workerID, apperror.Internal("ids result key", err))
	}
	if err := s.storage.Put(ctx, key, bytes.NewReader(blob), "application/json", int64(len(blob))); err != nil {
		return s.failIDsJob(ctx, job, workerID, apperror.Internal("store ids result", err))
	}
	if err := s.bulk.SetResultKey(ctx, job.ID, key); err != nil {
		return err
	}
	return s.bulk.MarkTerminal(ctx, job.ID, workerID, models.BulkJobStatusCompleted, "", time.Now().UTC())
}

// failIDsJob marks an ids job failed (guarded on claimedBy) and returns the
// original error for the worker loop to log.
func (s *BulkJobService) failIDsJob(ctx context.Context, job *repository.BulkJobDoc, workerID string, cause error) error {
	s.logger.Error("bulk_job_ids_failed", slog.String("job", job.ID.Hex()), slog.Any("err", cause))
	_ = s.bulk.MarkTerminal(ctx, job.ID, workerID, models.BulkJobStatusFailed, cause.Error(), time.Now().UTC())
	return cause
}
