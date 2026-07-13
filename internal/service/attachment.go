package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/repository"
	"imp/internal/storage"
)

// AttachmentConfig carries the per-request and per-file limits enforced by
// the service.
type AttachmentConfig struct {
	MaxBytes      int64
	MaxPerRequest int
}

// attachmentInserter is the narrow slice of the repo the service needs for
// uploads. Kept as an interface so unit tests can stub without a live Mongo.
type attachmentInserter interface {
	Insert(ctx context.Context, a *models.Attachment) error
}

// attachmentReader is the narrow slice of the repo the service needs for
// action-time validation. Kept as an interface so unit tests can stub
// without a live Mongo.
type attachmentReader interface {
	FindByIDs(ctx context.Context, ids []bson.ObjectID) ([]models.Attachment, error)
}

// ValidationResult reports the outcome of attachment validation for an
// action request. OK is true iff both phases pass. On Phase-A failure the
// GateError is set and PerAttachment is empty. On Phase-B failure
// PerAttachment mirrors the input order.
type ValidationResult struct {
	OK            bool
	GateError     error
	PerAttachment []models.AttachmentValidationError
}

type AttachmentService struct {
	attachments attachmentInserter
	reader      attachmentReader
	// full repo (used by Link/OrphanSweep in later tasks).
	repo    *repository.AttachmentRepository
	assets  *repository.AssetRepository
	storage storage.FileStorage
	cfg     AttachmentConfig
}

func NewAttachmentService(
	repo *repository.AttachmentRepository,
	assets *repository.AssetRepository,
	fs storage.FileStorage,
	cfg AttachmentConfig,
) *AttachmentService {
	return &AttachmentService{
		attachments: repo,
		reader:      repo,
		repo:        repo,
		assets:      assets,
		storage:     fs,
		cfg:         cfg,
	}
}

var allowedContentTypes = map[string]struct{}{
	"image/jpeg":      {},
	"image/png":       {},
	"image/webp":      {},
	"application/pdf": {},
}

// Upload sniffs the first 512 bytes of r for content type, rejects if not
// in the allowed set or if the total stream would exceed cfg.MaxBytes,
// writes bytes to storage under a fresh key, and inserts an unlinked
// attachments doc. Returns the inserted attachment (linked=false).
func (s *AttachmentService) Upload(ctx context.Context, uploadedBy bson.ObjectID, filename string, r io.Reader) (*models.Attachment, error) {
	// Read up to 512 bytes to sniff content type.
	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, apperror.BadRequest(fmt.Sprintf("read upload: %v", err))
	}
	if n == 0 {
		return nil, apperror.BadRequest("empty file")
	}
	head = head[:n]

	// http.DetectContentType may append ";charset=utf-8" — strip parameters.
	sniffed := http.DetectContentType(head)
	base := sniffed
	if i := bytes.IndexByte([]byte(sniffed), ';'); i > 0 {
		base = sniffed[:i]
	}
	if _, ok := allowedContentTypes[base]; !ok {
		return nil, apperror.BadRequest(fmt.Sprintf("content type %q not allowed", base))
	}

	// Combine head with the rest for storage. Wrap in a LimitReader that
	// allows one byte past MaxBytes so we can detect overflow.
	body := io.MultiReader(bytes.NewReader(head), r)
	limited := &io.LimitedReader{R: body, N: s.cfg.MaxBytes + 1}

	// Count bytes streamed to storage.
	counter := &countingReader{r: limited}

	key, err := storage.NewKey()
	if err != nil {
		return nil, apperror.Internal("generate storage key", err)
	}
	if err := s.storage.Put(ctx, key, counter, base, -1); err != nil {
		return nil, apperror.Internal("write to storage", err)
	}
	if counter.n > s.cfg.MaxBytes {
		// Cleanup the oversized bytes.
		_ = s.storage.Delete(ctx, key)
		return nil, apperror.BadRequest(fmt.Sprintf("file exceeds max size %d bytes", s.cfg.MaxBytes))
	}

	att := &models.Attachment{
		Filename:    filename,
		ContentType: base,
		Size:        counter.n,
		StorageKey:  &key,
		UploadedBy:  uploadedBy,
		Linked:      false,
	}
	if err := s.attachments.Insert(ctx, att); err != nil {
		// Attempt to clean up bytes; sweep will get it on next run if we fail.
		_ = s.storage.Delete(ctx, key)
		return nil, err
	}
	return att, nil
}

// Validate runs Phase A (request-level gate) then Phase B (per-attachment
// checks). Empty ids is a no-op OK result.
func (s *AttachmentService) Validate(ctx context.Context, ids []bson.ObjectID, uploadedBy bson.ObjectID) (*ValidationResult, error) {
	if len(ids) == 0 {
		return &ValidationResult{OK: true}, nil
	}
	// Phase A.1 — count cap.
	if len(ids) > s.cfg.MaxPerRequest {
		return &ValidationResult{
			OK:        false,
			GateError: apperror.BadRequest(fmt.Sprintf("too many attachments (max %d)", s.cfg.MaxPerRequest)),
		}, nil
	}
	// Phase A.2 — duplicates.
	seen := make(map[bson.ObjectID]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			return &ValidationResult{
				OK:        false,
				GateError: apperror.BadRequest("duplicate attachmentId"),
			}, nil
		}
		seen[id] = struct{}{}
	}

	// Phase B — bulk fetch, build id→doc map, then walk input in order.
	docs, err := s.reader.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[bson.ObjectID]models.Attachment, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}

	out := make([]models.AttachmentValidationError, 0, len(ids))
	allOK := true
	for _, id := range ids {
		e := models.AttachmentValidationError{AttachmentId: id, Ok: true}
		doc, found := byID[id]
		switch {
		case !found:
			allOK = false
			e.Ok = false
			e.Error = validationErrorPtr(models.AttachmentValidationErrorErrorNotFound)
		case doc.UploadedBy != uploadedBy:
			allOK = false
			e.Ok = false
			e.Error = validationErrorPtr(models.AttachmentValidationErrorErrorNotOwner)
		case doc.Linked:
			allOK = false
			e.Ok = false
			e.Error = validationErrorPtr(models.AttachmentValidationErrorErrorAlreadyLinked)
		}
		out = append(out, e)
	}
	return &ValidationResult{OK: allOK, PerAttachment: out}, nil
}

// validationErrorPtr returns a pointer to v — a small helper so callers can
// take the address of an enum constant inline.
func validationErrorPtr(v models.AttachmentValidationErrorError) *models.AttachmentValidationErrorError {
	return &v
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// orphanSweepMaxBatch caps a single sweep run so it doesn't page all
// attachments if the collection is huge.
const orphanSweepMaxBatch = 500

// orphanSweepAge is the minimum age before an unlinked attachment is
// considered abandoned and swept.
const orphanSweepAge = 24 * time.Hour

// Fetch returns an attachment by ID (any linked-state). Callers do their
// own RBAC — this helper is a passthrough to keep the handler thin.
func (s *AttachmentService) Fetch(ctx context.Context, id bson.ObjectID) (*models.Attachment, error) {
	return s.repo.GetByID(ctx, id)
}

// LoadForRBAC batch-loads the assets referenced by an attachment's assetIds
// so the handler can evaluate venue-scoped or custody-based access. Returns
// an empty slice for a doc with no assetIds (unlinked or malformed).
func (s *AttachmentService) LoadForRBAC(ctx context.Context, att *models.Attachment) ([]models.Asset, error) {
	if att.AssetIDs == nil || len(*att.AssetIDs) == 0 {
		return []models.Asset{}, nil
	}
	return s.assets.FindByIDs(ctx, *att.AssetIDs)
}

// FindByIDs batch-fetches attachments by IDs. Passthrough to the repository.
func (s *AttachmentService) FindByIDs(ctx context.Context, ids []bson.ObjectID) ([]models.Attachment, error) {
	return s.repo.FindByIDs(ctx, ids)
}

// GetBytes opens a reader over the stored bytes for the given storage key.
// Passthrough to keep the handler storage-agnostic.
func (s *AttachmentService) GetBytes(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.storage.Get(ctx, key)
}

// Link marks the given attachments as linked to (assetID, movementID). Empty
// ids is a no-op. If called inside a WithTransaction callback with a
// session-scoped ctx, the update participates in that transaction.
func (s *AttachmentService) Link(ctx context.Context, ids []bson.ObjectID, assetID, movementID bson.ObjectID) error {
	if len(ids) == 0 || s.repo == nil {
		return nil
	}
	return s.repo.MarkLinked(ctx, ids, assetID, movementID)
}

// ReserveForJob marks attachments as held by an async bulk job so the orphan
// sweep skips them until the job reaches a terminal state. Empty ids is a
// no-op. Called at enqueue.
func (s *AttachmentService) ReserveForJob(ctx context.Context, ids []bson.ObjectID, jobID bson.ObjectID) error {
	if len(ids) == 0 || s.repo == nil {
		return nil
	}
	return s.repo.Reserve(ctx, ids, jobID)
}

// ReleaseJobReservation clears a job's attachment reservation once it is
// terminal. Linked attachments stay linked; unlinked ones become sweepable.
func (s *AttachmentService) ReleaseJobReservation(ctx context.Context, jobID bson.ObjectID) error {
	if s.repo == nil {
		return nil
	}
	return s.repo.ReleaseReservation(ctx, jobID)
}

// OrphanSweep deletes attachments that are unlinked and older than
// orphanSweepAge, along with their stored bytes. Per-item errors are
// logged and skipped so one bad row never aborts the batch.
func (s *AttachmentService) OrphanSweep(ctx context.Context) error {
	if s.repo == nil {
		return nil
	}
	cutoff := time.Now().UTC().Add(-orphanSweepAge)
	orphans, err := s.repo.ListOrphans(ctx, cutoff, orphanSweepMaxBatch)
	if err != nil {
		return err
	}
	for _, o := range orphans {
		// Handle nil storageKey defensively — skip bytes delete but still delete doc.
		if o.StorageKey != nil {
			if err := s.storage.Delete(ctx, *o.StorageKey); err != nil {
				slog.Error("orphan_sweep_delete_bytes_failed",
					slog.String("attachment_id", o.ID.Hex()),
					slog.String("storage_key", *o.StorageKey),
					slog.Any("err", err),
				)
				// Continue to doc delete attempt.
			}
		} else {
			slog.Warn("orphan_sweep_nil_storage_key",
				slog.String("attachment_id", o.ID.Hex()),
			)
		}
		if err := s.repo.Delete(ctx, o.ID); err != nil {
			slog.Error("orphan_sweep_delete_doc_failed",
				slog.String("attachment_id", o.ID.Hex()),
				slog.Any("err", err),
			)
			continue
		}
	}
	return nil
}
