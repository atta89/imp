package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"image"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/notification"
	"imp/internal/qr"
	"imp/internal/repository"
)

type AssetService struct {
	assets      *repository.AssetRepository
	movements   *repository.MovementRepository
	venues      *repository.VenueRepository
	categories  *repository.CategoryRepository
	users       *repository.UserRepository
	departments *repository.DepartmentRepository
	counters    *repository.CounterRepository
	triggers    *notification.Triggers
	client      *mongo.Client
	baseURL     string      // FRONTEND_BASE_URL — host of the web app; /scan/<token> URLs encoded in QR codes resolve here, not at the API
	qrLogo      image.Image // optional center logo for QR images; nil = plain QR
	attachments *AttachmentService
}

func NewAssetService(
	assets *repository.AssetRepository,
	movements *repository.MovementRepository,
	venues *repository.VenueRepository,
	categories *repository.CategoryRepository,
	users *repository.UserRepository,
	departments *repository.DepartmentRepository,
	counters *repository.CounterRepository,
	triggers *notification.Triggers,
	client *mongo.Client,
	baseURL string,
	qrLogo image.Image,
	attachments *AttachmentService,
) *AssetService {
	return &AssetService{
		assets:      assets,
		movements:   movements,
		venues:      venues,
		categories:  categories,
		users:       users,
		departments: departments,
		counters:    counters,
		triggers:    triggers,
		client:      client,
		baseURL:     strings.TrimRight(baseURL, "/"),
		qrLogo:      qrLogo,
		attachments: attachments,
	}
}

// derefAttachmentIDs safely dereferences an optional attachmentIds pointer
// from a request struct. Nil pointer (field absent from the JSON body) →
// nil slice, treated identically to an explicit empty array.
func derefAttachmentIDs(p *[]bson.ObjectID) []bson.ObjectID {
	if p == nil {
		return nil
	}
	return *p
}

// AttachmentValidationFailure is a sentinel returned by asset services when
// per-attachment validation fails (Phase B). Handlers type-assert on this
// (via errors.As) to render the 400 AttachmentValidationResponse body
// per §5 of the design; it is distinct from a Phase-A gate failure, which
// surfaces as a plain *apperror.Error instead.
type AttachmentValidationFailure struct {
	PerAttachment []models.AttachmentValidationError
}

func (e *AttachmentValidationFailure) Error() string { return "attachment validation failed" }

// validateAttachments runs attachment validation for an action request.
// Empty ids is a no-op (nil). A Phase-A gate failure (too many attachments,
// duplicate ids) returns the underlying apperror directly. A Phase-B failure
// (not found / not owner / already linked) returns an
// *AttachmentValidationFailure carrying the per-attachment diagnostics.
func (s *AssetService) validateAttachments(ctx context.Context, ids []bson.ObjectID, uploadedBy bson.ObjectID) error {
	if len(ids) == 0 {
		return nil
	}
	res, err := s.attachments.Validate(ctx, ids, uploadedBy)
	if err != nil {
		return err
	}
	if res.OK {
		return nil
	}
	if res.GateError != nil {
		return res.GateError
	}
	return &AttachmentValidationFailure{PerAttachment: res.PerAttachment}
}

// AssetListQuery is the parsed filter for GET /assets.
type AssetListQuery struct {
	Venue        *bson.ObjectID // home venue
	CurrentVenue *bson.ObjectID
	Category     *bson.ObjectID
	Department   *bson.ObjectID
	Status       models.AssetStatus // empty = no filter
	Responsible  *bson.ObjectID
	Away         bool
	Overdue      bool
	Q            string
	Scope        []bson.ObjectID // non-admin venue scope; nil for admins (no filter)
}

func (s *AssetService) Create(ctx context.Context, in models.CreateAssetRequest) (*models.Asset, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, apperror.BadRequest("name is required")
	}
	if !validCondition(in.Condition) {
		return nil, apperror.BadRequest("invalid condition")
	}

	cat, err := s.categories.FindByID(ctx, in.CategoryID)
	if err != nil {
		return nil, err
	}
	if _, err := s.venues.FindByID(ctx, in.HomeVenueID); err != nil {
		return nil, err
	}
	currentVenue := in.HomeVenueID
	if in.CurrentVenueID != nil {
		if _, err := s.venues.FindByID(ctx, *in.CurrentVenueID); err != nil {
			return nil, err
		}
		currentVenue = *in.CurrentVenueID
	}
	if in.ResponsibleUserID != nil {
		if _, err := s.users.FindByID(ctx, *in.ResponsibleUserID); err != nil {
			return nil, err
		}
	}
	if err := ValidateAssetDepartment(ctx, s.departments, in.DepartmentID, in.HomeVenueID); err != nil {
		return nil, err
	}

	tag, err := nextAssetTag(ctx, s.counters, cat.Slug)
	if err != nil {
		return nil, err
	}
	token, err := generateQRToken()
	if err != nil {
		return nil, apperror.Internal("generate qr token", err)
	}

	a := &models.Asset{
		AssetTag:          tag,
		QrToken:           token,
		Name:              strings.TrimSpace(in.Name),
		CategoryID:        in.CategoryID,
		HomeVenueID:       in.HomeVenueID,
		DepartmentID:      in.DepartmentID,
		CurrentVenueID:    currentVenue,
		Status:            models.Available,
		Condition:         in.Condition,
		ResponsibleUserID: in.ResponsibleUserID,
		PurchaseOrderID:   in.PurchaseOrderID,
		PurchaseDate:      in.PurchaseDate,
		SerialNumber:      in.SerialNumber,
		Specs:             in.Specs,
		Photos:            in.Photos,
		Notes:             in.Notes,
		IsOverdue:         false,
		IsActive:          true,
	}
	if err := s.assets.Create(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *AssetService) Get(ctx context.Context, id bson.ObjectID) (*models.Asset, error) {
	return s.assets.FindByID(ctx, id)
}

func (s *AssetService) List(ctx context.Context, q AssetListQuery, page, limit int) ([]models.Asset, int64, error) {
	return s.assets.List(ctx, buildAssetFilter(q), page, limit)
}

func (s *AssetService) Update(ctx context.Context, id bson.ObjectID, in models.UpdateAssetRequest) (*models.Asset, error) {
	existing, err := s.assets.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	set := bson.M{}
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return nil, apperror.BadRequest("name cannot be empty")
		}
		set["name"] = strings.TrimSpace(*in.Name)
	}
	if in.CategoryID != nil {
		if _, err := s.categories.FindByID(ctx, *in.CategoryID); err != nil {
			return nil, err
		}
		set["categoryId"] = *in.CategoryID
	}

	newHomeVenue := existing.HomeVenueID
	newDeptID := existing.DepartmentID // pointer
	if in.HomeVenueID != nil {
		if _, err := s.venues.FindByID(ctx, *in.HomeVenueID); err != nil {
			return nil, err
		}
		newHomeVenue = *in.HomeVenueID
		set["homeVenueId"] = *in.HomeVenueID
	}
	// UpdateAssetRequest.DepartmentID is a pointer. But we cannot distinguish
	// "clear" from "unset" via a plain *ObjectID. Convention here: if the
	// request body includes a departmentId key with a valid ObjectID → set to
	// that value; the API does NOT support clearing a department via PUT in
	// v1 (users can transfer or update homeVenueId which rejects the edit
	// per spec if the current department no longer belongs to the new home).
	if in.DepartmentID != nil {
		newDeptID = in.DepartmentID
		set["departmentId"] = *in.DepartmentID
	}
	if in.HomeVenueID != nil || in.DepartmentID != nil {
		if err := ValidateAssetDepartment(ctx, s.departments, newDeptID, newHomeVenue); err != nil {
			return nil, err
		}
	}

	if in.Condition != nil {
		if !validCondition(*in.Condition) {
			return nil, apperror.BadRequest("invalid condition")
		}
		set["condition"] = *in.Condition
	}
	if in.SerialNumber != nil {
		set["serialNumber"] = *in.SerialNumber
	}
	if in.Specs != nil {
		set["specs"] = *in.Specs
	}
	if in.Photos != nil {
		set["photos"] = *in.Photos
	}
	if in.Notes != nil {
		set["notes"] = *in.Notes
	}
	if in.IsActive != nil {
		set["isActive"] = *in.IsActive
	}
	if len(set) == 0 {
		return s.assets.FindByID(ctx, id)
	}
	return s.assets.Update(ctx, id, set)
}

func (s *AssetService) Delete(ctx context.Context, id bson.ObjectID) error {
	return s.assets.Delete(ctx, id)
}

func (s *AssetService) History(ctx context.Context, id bson.ObjectID) ([]models.Movement, error) {
	if _, err := s.assets.FindByID(ctx, id); err != nil {
		return nil, err
	}
	movements, err := s.movements.ListByAsset(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.enrichWithAttachments(ctx, movements)
}

// enrichWithAttachments populates the Attachments field on movements by
// batch-fetching all referenced attachment documents. Movements with no
// AttachmentIDs remain unchanged (Attachments stays nil).
func (s *AssetService) enrichWithAttachments(ctx context.Context, movements []models.Movement) ([]models.Movement, error) {
	// Collect unique attachment IDs across all movements.
	seen := make(map[bson.ObjectID]struct{})
	var allIDs []bson.ObjectID
	for _, m := range movements {
		if m.AttachmentIDs == nil || len(*m.AttachmentIDs) == 0 {
			continue
		}
		for _, id := range *m.AttachmentIDs {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			allIDs = append(allIDs, id)
		}
	}
	// No attachments to fetch.
	if len(allIDs) == 0 {
		return movements, nil
	}

	// Batch fetch all attachment docs.
	docs, err := s.attachments.FindByIDs(ctx, allIDs)
	if err != nil {
		return nil, err
	}

	// Build map id → Attachment for O(1) lookup.
	byID := make(map[bson.ObjectID]models.Attachment, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}

	// Enrich each movement: walk its AttachmentIDs and build the
	// Attachments slice in order.
	out := make([]models.Movement, len(movements))
	for i, m := range movements {
		if m.AttachmentIDs == nil || len(*m.AttachmentIDs) == 0 {
			// No attachments for this movement.
			out[i] = m
			continue
		}

		// Build AttachmentMeta slice matching the order of AttachmentIDs.
		metas := make([]models.AttachmentMeta, 0, len(*m.AttachmentIDs))
		for _, id := range *m.AttachmentIDs {
			if d, ok := byID[id]; ok {
				metas = append(metas, models.AttachmentMeta{
					Id:          d.ID,
					Filename:    d.Filename,
					ContentType: d.ContentType,
					Size:        d.Size,
				})
			}
			// Missing docs are silently skipped (defensive; shouldn't happen).
		}

		// Set the Attachments field on the movement.
		m.Attachments = &metas
		out[i] = m
	}

	return out, nil
}

// ChangeStatus validates the state-machine transition, updates the asset, and
// appends a status_change movement. Runs asset+movement (+attachment link)
// atomically in a Mongo transaction.
func (s *AssetService) ChangeStatus(ctx context.Context, id, performedBy bson.ObjectID, in models.StatusChangeRequest) (*models.Asset, error) {
	a, err := s.assets.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	attachmentIDs := derefAttachmentIDs(in.AttachmentIDs)
	if err := s.validateAttachments(ctx, attachmentIDs, performedBy); err != nil {
		return nil, err
	}

	sess, err := s.client.StartSession()
	if err != nil {
		return nil, apperror.Internal("start session", err)
	}
	defer sess.EndSession(ctx)

	var updated *models.Asset
	_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		u, err := s.applyStatusChange(sc, a, performedBy, in, attachmentIDs)
		if err != nil {
			return nil, err
		}
		updated = u
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *AssetService) applyStatusChange(ctx context.Context, a *models.Asset, performedBy bson.ObjectID, in models.StatusChangeRequest, attachmentIDs []bson.ObjectID) (*models.Asset, error) {
	if !IsAllowedTransition(a.Status, in.Status) {
		return nil, apperror.Conflict(fmt.Sprintf("cannot transition asset from %s to %s", a.Status, in.Status))
	}
	updated, err := s.assets.Update(ctx, a.ID, bson.M{"status": in.Status})
	if err != nil {
		return nil, err
	}
	from, to := a.Status, in.Status
	m := &models.Movement{
		AssetID:     a.ID,
		Type:        models.MovementTypeStatusChange,
		FromStatus:  &from,
		ToStatus:    &to,
		Reason:      in.Reason,
		PerformedBy: performedBy,
	}
	if len(attachmentIDs) > 0 {
		m.AttachmentIDs = &attachmentIDs
	}
	if err := s.movements.Create(ctx, m); err != nil {
		return nil, err
	}
	if err := s.attachments.Link(ctx, attachmentIDs, a.ID, m.ID); err != nil {
		return nil, err
	}
	return updated, nil
}

// UpdateCondition records a condition change: it updates asset.condition and
// appends a condition_change movement in the same MongoDB transaction so the
// asset and its audit row cannot diverge. Independent of status/repair — the
// caller decides whether to open a repair separately.
func (s *AssetService) UpdateCondition(ctx context.Context, id, performedBy bson.ObjectID, in models.ConditionUpdate) (*models.Asset, error) {
	a, err := s.assets.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := validateConditionChange(a.Condition, in.Condition); err != nil {
		return nil, err
	}

	attachmentIDs := derefAttachmentIDs(in.AttachmentIDs)
	if err := s.validateAttachments(ctx, attachmentIDs, performedBy); err != nil {
		return nil, err
	}

	sess, err := s.client.StartSession()
	if err != nil {
		return nil, apperror.Internal("start session", err)
	}
	defer sess.EndSession(ctx)

	var updated *models.Asset
	_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		u, err := s.applyConditionUpdate(sc, a, performedBy, in, attachmentIDs)
		if err != nil {
			return nil, err
		}
		updated = u
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// updateConditionForBulk is the txn-only variant of UpdateCondition used by
// BulkUpdateCondition. It performs no attachment validation — the caller
// (BulkUpdateCondition) validates the batch's shared attachment set ONCE
// up front, because re-validating per item would fail on the second item
// onward (the first item's Link already flips those attachments to
// already_linked). FindByID stays per-item so the race check (asset
// deleted/changed between planning and applying) is preserved.
func (s *AssetService) updateConditionForBulk(ctx context.Context, id, performedBy bson.ObjectID, in models.ConditionUpdate, attachmentIDs []bson.ObjectID) (*models.Asset, error) {
	a, err := s.assets.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := validateConditionChange(a.Condition, in.Condition); err != nil {
		return nil, err
	}

	sess, err := s.client.StartSession()
	if err != nil {
		return nil, apperror.Internal("start session", err)
	}
	defer sess.EndSession(ctx)

	var updated *models.Asset
	_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		u, err := s.applyConditionUpdate(sc, a, performedBy, in, attachmentIDs)
		if err != nil {
			return nil, err
		}
		updated = u
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// applyConditionUpdate updates asset.condition and appends a
// condition_change movement. Extracted from UpdateCondition so the bare
// write logic is unit-testable and consistent in shape with the other
// apply* helpers.
func (s *AssetService) applyConditionUpdate(ctx context.Context, a *models.Asset, performedBy bson.ObjectID, in models.ConditionUpdate, attachmentIDs []bson.ObjectID) (*models.Asset, error) {
	from, to := a.Condition, in.Condition
	updated, err := s.assets.Update(ctx, a.ID, bson.M{"condition": to})
	if err != nil {
		return nil, err
	}
	m := &models.Movement{
		AssetID:       a.ID,
		Type:          models.MovementTypeConditionChange,
		FromCondition: &from,
		ToCondition:   &to,
		Notes:         in.Notes,
		PerformedBy:   performedBy,
	}
	if len(attachmentIDs) > 0 {
		m.AttachmentIDs = &attachmentIDs
	}
	if err := s.movements.Create(ctx, m); err != nil {
		return nil, err
	}
	if err := s.attachments.Link(ctx, attachmentIDs, a.ID, m.ID); err != nil {
		return nil, err
	}
	return updated, nil
}

// Transfer changes the asset's currentVenueId. Transferring to the home venue
// is the "return home" action: it clears expectedReturnDate and isOverdue.
// Non-home transfers may carry an expectedReturnDate (temporary loan).
func (s *AssetService) Transfer(ctx context.Context, id, performedBy bson.ObjectID, in models.TransferAssetRequest) (*models.Asset, error) {
	a, err := s.assets.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.venues.FindByID(ctx, in.ToVenueID); err != nil {
		return nil, err
	}

	attachmentIDs := derefAttachmentIDs(in.AttachmentIDs)
	if err := s.validateAttachments(ctx, attachmentIDs, performedBy); err != nil {
		return nil, err
	}

	sess, err := s.client.StartSession()
	if err != nil {
		return nil, apperror.Internal("start session", err)
	}
	defer sess.EndSession(ctx)

	var updated *models.Asset
	_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		u, err := s.applyTransfer(sc, a, performedBy, in, attachmentIDs)
		if err != nil {
			return nil, err
		}
		updated = u
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	s.triggers.AssetTransferred(ctx, updated, a.CurrentVenueID, in.ToVenueID)
	return updated, nil
}

// applyTransfer changes currentVenueId (and returns-home clears
// expectedReturnDate + isOverdue). It MUST NOT touch departmentId — that is
// the invariant behind the department integrity rule; transfers don't affect
// which department "owns" the asset. Do not add departmentId to the set map.
func (s *AssetService) applyTransfer(ctx context.Context, a *models.Asset, performedBy bson.ObjectID, in models.TransferAssetRequest, attachmentIDs []bson.ObjectID) (*models.Asset, error) {
	if a.CurrentVenueID == in.ToVenueID {
		return nil, apperror.Conflict("asset is already at that venue")
	}
	set := bson.M{"currentVenueId": in.ToVenueID, "isOverdue": false}
	var expectedReturn *time.Time
	if in.ToVenueID == a.HomeVenueID {
		set["expectedReturnDate"] = nil
	} else if in.ExpectedReturnDate != nil {
		set["expectedReturnDate"] = *in.ExpectedReturnDate
		expectedReturn = in.ExpectedReturnDate
	}
	updated, err := s.assets.Update(ctx, a.ID, set)
	if err != nil {
		return nil, err
	}
	from, to := a.CurrentVenueID, in.ToVenueID
	m := &models.Movement{
		AssetID:            a.ID,
		Type:               models.MovementTypeTransfer,
		FromVenueID:        &from,
		ToVenueID:          &to,
		ExpectedReturnDate: expectedReturn,
		Notes:              in.Notes,
		PerformedBy:        performedBy,
	}
	if len(attachmentIDs) > 0 {
		m.AttachmentIDs = &attachmentIDs
	}
	if err := s.movements.Create(ctx, m); err != nil {
		return nil, err
	}
	if err := s.attachments.Link(ctx, attachmentIDs, a.ID, m.ID); err != nil {
		return nil, err
	}
	return updated, nil
}

// AssignCustody reassigns the asset's responsible user and writes a
// custody_change movement. Also enqueues a per-asset email to the new
// custodian (PRD §6.11). The bulk path (BulkAssign) uses the same
// applyAssignCustody core but folds the emails into a single digest.
func (s *AssetService) AssignCustody(ctx context.Context, id, performedBy bson.ObjectID, in models.AssignCustodyRequest) (*models.Asset, error) {
	a, err := s.assets.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	u, err := s.users.FindByID(ctx, in.ResponsibleUserID)
	if err != nil {
		return nil, err
	}
	if !u.IsActive {
		return nil, apperror.BadRequest("user is not active")
	}
	if a.ResponsibleUserID != nil && *a.ResponsibleUserID == in.ResponsibleUserID {
		return a, nil // no-op — must run BEFORE attachment validation so a no-op action doesn't consume attachments
	}

	attachmentIDs := derefAttachmentIDs(in.AttachmentIDs)
	if err := s.validateAttachments(ctx, attachmentIDs, performedBy); err != nil {
		return nil, err
	}

	sess, err := s.client.StartSession()
	if err != nil {
		return nil, apperror.Internal("start session", err)
	}
	defer sess.EndSession(ctx)

	var updated *models.Asset
	_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		u, err := s.applyAssignCustody(sc, a, performedBy, in.ResponsibleUserID, in.Notes, attachmentIDs)
		if err != nil {
			return nil, err
		}
		updated = u
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	s.triggers.CustodyAssigned(ctx, updated, in.ResponsibleUserID)
	return updated, nil
}

// applyAssignCustody is the core custody-change write shared by the single
// and bulk paths: update responsibleUserId and insert one custody_change
// movement. Callers own the pre-checks (user exists+active, no-op), the
// transaction boundary, and any notification enqueueing.
func (s *AssetService) applyAssignCustody(ctx context.Context, a *models.Asset, performedBy, newUserID bson.ObjectID, notes *string, attachmentIDs []bson.ObjectID) (*models.Asset, error) {
	updated, err := s.assets.Update(ctx, a.ID, bson.M{"responsibleUserId": newUserID})
	if err != nil {
		return nil, err
	}
	m := buildCustodyMovement(a, performedBy, newUserID, notes)
	if len(attachmentIDs) > 0 {
		m.AttachmentIDs = &attachmentIDs
	}
	if err := s.movements.Create(ctx, m); err != nil {
		return nil, err
	}
	if err := s.attachments.Link(ctx, attachmentIDs, a.ID, m.ID); err != nil {
		return nil, err
	}
	return updated, nil
}

// buildCustodyMovement returns the Movement struct for a custody change.
// Pulled out so the from/to/type wiring is unit-testable without a repo.
func buildCustodyMovement(a *models.Asset, performedBy, newUserID bson.ObjectID, notes *string) *models.Movement {
	to := newUserID
	return &models.Movement{
		AssetID:     a.ID,
		Type:        models.MovementTypeCustodyChange,
		FromUserID:  a.ResponsibleUserID,
		ToUserID:    &to,
		Notes:       notes,
		PerformedBy: performedBy,
	}
}

// ScanURL returns the /scan/<token> URL encoded in an asset's QR. The URL
// format is stable (printed labels depend on it); only the endpoint's access
// rules changed — it now requires an authenticated, authorized caller.
func (s *AssetService) ScanURL(token string) string {
	return s.baseURL + "/scan/" + token
}

// QRPNG renders the asset's QR code as a PNG. When a logo is configured the
// QR is generated at error-correction level Highest with the logo composited
// dead-center on a padded backing.
func (s *AssetService) QRPNG(ctx context.Context, id bson.ObjectID) ([]byte, error) {
	a, err := s.assets.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	url := s.ScanURL(a.QrToken)
	if s.qrLogo == nil {
		out, err := qr.PNG(url, 1024)
		if err != nil {
			return nil, apperror.Internal("render qr png", err)
		}
		return out, nil
	}
	out, err := qr.PNGWithLogo(url, 1024, s.qrLogo, qr.LogoOptions{})
	if err != nil {
		return nil, apperror.Internal("render qr png with logo", err)
	}
	return out, nil
}

// QRBulkPDF renders one printable PDF with QR labels for the given assets.
// Unknown ids → 400 with the offending id (validation happens up-front so
// the printed sheet always matches the request). RBAC: caller must be admin
// or have either home OR current venue in scope for every requested asset.
func (s *AssetService) QRBulkPDF(ctx context.Context, p Principal, ids []bson.ObjectID) ([]byte, error) {
	if len(ids) == 0 {
		return nil, apperror.BadRequest("at least one asset id is required")
	}
	if len(ids) > MaxBulkAssets {
		return nil, apperror.BadRequest(fmt.Sprintf("batch exceeds MaxBulkAssets (%d)", MaxBulkAssets))
	}
	assets, err := s.assets.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	// RBAC: every requested asset must be in the caller's scope.
	for i := range assets {
		a := &assets[i]
		if !p.canAccessVenue(a.HomeVenueID) && !p.canAccessVenue(a.CurrentVenueID) {
			return nil, apperror.Forbidden("not authorized for asset " + a.AssetTag)
		}
	}

	// Bulk-load venue + category names so we don't N+1 the database.
	venueNames := map[bson.ObjectID]string{}
	categoryNames := map[bson.ObjectID]string{}
	for i := range assets {
		venueNames[assets[i].HomeVenueID] = ""
		categoryNames[assets[i].CategoryID] = ""
	}
	for id := range venueNames {
		if v, err := s.venues.FindByID(ctx, id); err == nil {
			venueNames[id] = v.Name
		}
	}
	for id := range categoryNames {
		if c, err := s.categories.FindByID(ctx, id); err == nil {
			categoryNames[id] = c.Name
		}
	}

	labels := make([]qr.Label, 0, len(assets))
	for i := range assets {
		a := &assets[i]
		labels = append(labels, qr.Label{
			Content:   s.ScanURL(a.QrToken),
			Tag:       a.AssetTag,
			Name:      a.Name,
			Category:  categoryNames[a.CategoryID],
			HomeVenue: venueNames[a.HomeVenueID],
		})
	}
	out, err := qr.LabelsPDFWith(labels, qr.LabelsPDFOptions{Logo: s.qrLogo})
	if err != nil {
		return nil, apperror.Internal("render labels pdf", err)
	}
	return out, nil
}

// ScanView resolves a scanned QR token into a ScanAssetView for an
// authenticated, authorized caller. It loads the asset first (unknown token →
// 404) and only then authorizes: the caller must be an admin, have venue scope
// on the asset's home or current venue, or be its current custodian
// (out-of-scope caller → 403). The custodian's full contact details are
// included — every viewer is now authenticated AND authorized. Resolves for
// lost/retired assets too (PRD §6.4).
func (s *AssetService) ScanView(ctx context.Context, p Principal, qrToken string) (*models.ScanAssetView, error) {
	a, err := s.assets.FindByQRToken(ctx, qrToken)
	if err != nil {
		return nil, err
	}
	if !p.CanAccessAsset(a) {
		return nil, apperror.Forbidden("not authorized for this asset")
	}
	view := &models.ScanAssetView{Asset: *a}

	if home, err := s.venues.FindByID(ctx, a.HomeVenueID); err == nil {
		view.HomeVenueName = &home.Name
	}
	if a.CurrentVenueID == a.HomeVenueID {
		view.CurrentVenueName = view.HomeVenueName
	} else if cur, err := s.venues.FindByID(ctx, a.CurrentVenueID); err == nil {
		view.CurrentVenueName = &cur.Name
	}
	if cat, err := s.categories.FindByID(ctx, a.CategoryID); err == nil {
		view.CategoryName = &cat.Name
	}
	if a.DepartmentID != nil {
		if d, err := s.departments.FindByID(ctx, *a.DepartmentID); err == nil {
			view.DepartmentName = &d.Name
		}
	}
	if a.ResponsibleUserID != nil {
		if u, err := s.users.FindByID(ctx, *a.ResponsibleUserID); err == nil {
			view.ResponsiblePerson = &models.ScanUserContact{
				ID:       u.ID,
				Name:     u.Name,
				Role:     u.Role,
				Position: u.Position,
				Email:    u.Email,
				Phone:    u.Phone,
			}
		}
	}
	return view, nil
}

// nextAssetTag returns the next per-prefix asset tag (e.g. "LAP-0001"). The
// prefix is derived from the category slug: uppercased first 3 chars, padded
// with "X" if shorter. Package-level so both AssetService and the PO-receive
// flow can reuse it.
func nextAssetTag(ctx context.Context, counters *repository.CounterRepository, slug string) (string, error) {
	prefix := assetTagPrefix(slug)
	seq, err := counters.Next(ctx, prefix)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%04d", prefix, seq), nil
}

func assetTagPrefix(slug string) string {
	p := strings.ToUpper(strings.TrimSpace(slug))
	if p == "" {
		return "AST"
	}
	if len(p) >= 3 {
		return p[:3]
	}
	return (p + "XXX")[:3]
}

// generateQRToken returns a 22-char URL-safe random string (128 bits of
// entropy) — non-enumerable, safe to put in a public URL.
func generateQRToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func validCondition(c models.AssetCondition) bool {
	switch c {
	case models.New, models.Good, models.Fair, models.Poor:
		return true
	}
	return false
}

// buildAssetFilter combines list-query parameters and the requester's venue
// scope into a single Mongo filter document.
func buildAssetFilter(q AssetListQuery) bson.M {
	var clauses []bson.M

	if q.Venue != nil {
		clauses = append(clauses, bson.M{"homeVenueId": *q.Venue})
	}
	if q.CurrentVenue != nil {
		clauses = append(clauses, bson.M{"currentVenueId": *q.CurrentVenue})
	}
	if q.Category != nil {
		clauses = append(clauses, bson.M{"categoryId": *q.Category})
	}
	if q.Department != nil {
		clauses = append(clauses, bson.M{"departmentId": *q.Department})
	}
	if q.Status != "" {
		clauses = append(clauses, bson.M{"status": q.Status})
	}
	if q.Responsible != nil {
		clauses = append(clauses, bson.M{"responsibleUserId": *q.Responsible})
	}
	if q.Away {
		clauses = append(clauses, bson.M{"$expr": bson.M{"$ne": []string{"$homeVenueId", "$currentVenueId"}}})
	}
	if q.Overdue {
		clauses = append(clauses, bson.M{"isOverdue": true})
	}
	if q.Q != "" {
		rx := bson.M{"$regex": regexEscape(q.Q), "$options": "i"}
		clauses = append(clauses, bson.M{"$or": []bson.M{
			{"name": rx},
			{"assetTag": rx},
			{"serialNumber": rx},
		}})
	}
	if q.Scope != nil {
		clauses = append(clauses, bson.M{"$or": []bson.M{
			{"homeVenueId": bson.M{"$in": q.Scope}},
			{"currentVenueId": bson.M{"$in": q.Scope}},
		}})
	}

	switch len(clauses) {
	case 0:
		return bson.M{}
	case 1:
		return clauses[0]
	default:
		return bson.M{"$and": clauses}
	}
}

// regexEscape returns a literal-match version of s safe to embed in $regex.
func regexEscape(s string) string {
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
