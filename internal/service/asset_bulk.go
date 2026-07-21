package service

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/notification"
	"imp/internal/repository"
)

// MaxBulkAssets is the DEFAULT per-request asset cap for the synchronous bulk
// endpoints. The effective cap is AssetService.maxBulkAssets, wired from
// BULK_MAX_ASSETS at construction; this const is the fallback used when config
// is zero and the value the pure validation helpers are exercised with in tests.
const MaxBulkAssets = 5000

// Principal is the request principal flattened into plain data so the pure
// authorization/validation helpers stay testable without Fiber.
type Principal struct {
	IsAdmin  bool
	UserID   bson.ObjectID
	VenueIDs map[string]struct{}
}

func (p Principal) canAccessVenue(id bson.ObjectID) bool {
	if p.IsAdmin {
		return true
	}
	_, ok := p.VenueIDs[id.Hex()]
	return ok
}

// CanAccessAsset reports whether the principal may view the asset: admins
// always; otherwise the asset's home OR current venue must be in the
// principal's scope, or the principal must be the asset's current custodian
// (responsibleUserId). Shared by the authenticated scan path and the
// GET /attachments/{id}/download RBAC so the two rules cannot drift.
func (p Principal) CanAccessAsset(a *models.Asset) bool {
	if p.canAccessVenue(a.HomeVenueID) || p.canAccessVenue(a.CurrentVenueID) {
		return true
	}
	return a.ResponsibleUserID != nil && *a.ResponsibleUserID == p.UserID
}

// validatedTransfer / validatedStatus / validatedAssign hold the asset + the
// per-row inputs the bulk apply step needs. They are intentionally NOT
// exported.
type validatedTransfer struct {
	asset *models.Asset
}
type validatedStatus struct {
	asset *models.Asset
}

// validatedAssign carries an asset that survived batch validation. noOp=true
// means responsibleUserId already matches the target — the row is counted as
// skippedNoOp and NO update or movement is written for it.
type validatedAssign struct {
	asset *models.Asset
	noOp  bool
}

// notifyTarget is a (homeVenue, [assetsMoved]) tuple used to dedupe the
// transfer digest. One target per distinct home venue across the batch.
type notifyTarget struct {
	HomeVenueID bson.ObjectID
	Assets      []*models.Asset
}

func errString(s string) *string { return &s }

func validateBulkTransferRequest(
	in models.BulkTransferRequest,
	p Principal,
	lookup func(bson.ObjectID) (*models.Asset, error),
	destVenueExists bool,
	maxBulk int,
) ([]validatedTransfer, []models.BulkActionResult, bool) {
	n := len(in.AssetIDs)
	results := make([]models.BulkActionResult, 0, n)
	if n == 0 || n > maxBulk {
		// One synthetic failure row to signal the global problem; allOK=false.
		return nil, []models.BulkActionResult{{
			AssetID: bson.NilObjectID,
			Ok:      false,
			Error:   errString(fmt.Sprintf("batch size %d outside [1, %d]", n, maxBulk)),
		}}, false
	}
	if !p.canAccessVenue(in.ToVenueID) {
		// The destination is the same for every row — fail the whole batch up front.
		return nil, []models.BulkActionResult{{
			AssetID: bson.NilObjectID,
			Ok:      false,
			Error:   errString("dest_venue_forbidden"),
		}}, false
	}
	if !destVenueExists {
		return nil, []models.BulkActionResult{{
			AssetID: bson.NilObjectID,
			Ok:      false,
			Error:   errString("dest_venue_not_found"),
		}}, false
	}

	seen := make(map[bson.ObjectID]struct{}, n)
	oks := make([]validatedTransfer, 0, n)
	allOK := true
	for _, id := range in.AssetIDs {
		if _, dup := seen[id]; dup {
			results = append(results, models.BulkActionResult{AssetID: id, Ok: false, Error: errString("duplicate_id")})
			allOK = false
			continue
		}
		seen[id] = struct{}{}

		a, err := lookup(id)
		if err != nil || a == nil {
			results = append(results, models.BulkActionResult{AssetID: id, Ok: false, Error: errString("not_found")})
			allOK = false
			continue
		}
		if !p.canAccessVenue(a.HomeVenueID) && !p.canAccessVenue(a.CurrentVenueID) {
			results = append(results, models.BulkActionResult{AssetID: id, Ok: false, Error: errString("forbidden")})
			allOK = false
			continue
		}
		if a.CurrentVenueID == in.ToVenueID {
			results = append(results, models.BulkActionResult{AssetID: id, Ok: false, Error: errString("same_venue")})
			allOK = false
			continue
		}
		results = append(results, models.BulkActionResult{AssetID: id, Ok: true})
		oks = append(oks, validatedTransfer{asset: a})
	}
	return oks, results, allOK
}

func validateBulkStatusRequest(
	in models.BulkStatusRequest,
	p Principal,
	lookup func(bson.ObjectID) (*models.Asset, error),
	maxBulk int,
) ([]validatedStatus, []models.BulkActionResult, bool) {
	n := len(in.AssetIDs)
	if n == 0 || n > maxBulk {
		return nil, []models.BulkActionResult{{
			AssetID: bson.NilObjectID,
			Ok:      false,
			Error:   errString(fmt.Sprintf("batch size %d outside [1, %d]", n, maxBulk)),
		}}, false
	}

	results := make([]models.BulkActionResult, 0, n)
	seen := make(map[bson.ObjectID]struct{}, n)
	oks := make([]validatedStatus, 0, n)
	allOK := true
	for _, id := range in.AssetIDs {
		if _, dup := seen[id]; dup {
			results = append(results, models.BulkActionResult{AssetID: id, Ok: false, Error: errString("duplicate_id")})
			allOK = false
			continue
		}
		seen[id] = struct{}{}

		a, err := lookup(id)
		if err != nil || a == nil {
			results = append(results, models.BulkActionResult{AssetID: id, Ok: false, Error: errString("not_found")})
			allOK = false
			continue
		}
		if !p.canAccessVenue(a.HomeVenueID) && !p.canAccessVenue(a.CurrentVenueID) {
			results = append(results, models.BulkActionResult{AssetID: id, Ok: false, Error: errString("forbidden")})
			allOK = false
			continue
		}
		if !IsAllowedTransition(a.Status, in.Status) {
			results = append(results, models.BulkActionResult{AssetID: id, Ok: false, Error: errString("invalid_transition")})
			allOK = false
			continue
		}
		results = append(results, models.BulkActionResult{AssetID: id, Ok: true})
		oks = append(oks, validatedStatus{asset: a})
	}
	return oks, results, allOK
}

// dedupeTransferNotifyTargets groups validated transfers by home venue so the
// digest enqueues one outbox entry per home-venue-manager rather than N×M.
// Order: preserves first-appearance of each home venue.
func dedupeTransferNotifyTargets(oks []validatedTransfer, _ bson.ObjectID) []notifyTarget {
	pos := map[bson.ObjectID]int{}
	out := []notifyTarget{}
	for _, v := range oks {
		i, ok := pos[v.asset.HomeVenueID]
		if !ok {
			pos[v.asset.HomeVenueID] = len(out)
			out = append(out, notifyTarget{HomeVenueID: v.asset.HomeVenueID, Assets: []*models.Asset{v.asset}})
			continue
		}
		out[i].Assets = append(out[i].Assets, v.asset)
	}
	return out
}

// BulkTransfer validates the whole batch up front and, only if every row
// passes, applies the moves + writes movements in one Mongo transaction.
func (s *AssetService) BulkTransfer(ctx context.Context, performedBy bson.ObjectID, p Principal, in models.BulkTransferRequest) (*models.BulkActionResponse, error) {
	if n := len(in.AssetIDs); n == 0 {
		return nil, apperror.BadRequest("assetIds is required")
	} else if n > s.maxBulkAssets {
		return nil, apperror.BadRequest(fmt.Sprintf("batch exceeds MaxBulkAssets (%d)", s.maxBulkAssets))
	}

	attachmentIDs := derefAttachmentIDs(in.AttachmentIDs)
	if err := s.validateAttachments(ctx, attachmentIDs, performedBy); err != nil {
		return nil, err
	}

	// Confirm the destination venue exists once (it's shared across the batch).
	destExists := true
	if _, err := s.venues.FindByID(ctx, in.ToVenueID); err != nil {
		// A NotFound here becomes a per-batch "dest_venue_not_found" result —
		// validateBulkTransferRequest will surface it as the single failure row.
		if appErr, ok := apperror.As(err); !ok || appErr.Kind != apperror.KindNotFound {
			return nil, err
		}
		destExists = false
	}

	lookup := func(id bson.ObjectID) (*models.Asset, error) {
		return s.assets.FindByID(ctx, id)
	}
	oks, results, allOK := validateBulkTransferRequest(in, p, lookup, destExists, s.maxBulkAssets)
	if !allOK {
		return s.bulkResponse(results), nil
	}

	// Run the txn over the pre-validated set.
	sess, err := s.client.StartSession()
	if err != nil {
		return nil, apperror.Internal("start session", err)
	}
	defer sess.EndSession(ctx)

	_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		for _, v := range oks {
			if _, err := s.applyTransfer(sc, v.asset, performedBy, models.TransferAssetRequest{
				ToVenueID:          in.ToVenueID,
				ExpectedReturnDate: in.ExpectedReturnDate,
				Notes:              in.Notes,
			}, attachmentIDs); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, apperror.Internal("bulk transfer transaction", err)
	}

	// Enqueue digest emails AFTER the txn commits — the email row is in a
	// different collection and not part of the asset/movement atomic unit.
	groups := dedupeTransferNotifyTargets(oks, in.ToVenueID)
	bg := make([]notification.BulkTransferGroup, 0, len(groups))
	for _, g := range groups {
		refs := make([]notification.BulkTransferAssetRef, 0, len(g.Assets))
		for _, a := range g.Assets {
			refs = append(refs, notification.BulkTransferAssetRef{
				AssetID: a.ID, Tag: a.AssetTag, Name: a.Name, QRToken: a.QrToken,
			})
		}
		bg = append(bg, notification.BulkTransferGroup{HomeVenueID: g.HomeVenueID, Assets: refs})
	}
	s.triggers.BulkTransferDigest(ctx, bg, in.ToVenueID)

	return s.bulkResponse(results), nil
}

// BulkChangeStatus mirrors BulkTransfer for the status-change path. No
// notifications (the single-asset status path doesn't enqueue any either).
func (s *AssetService) BulkChangeStatus(ctx context.Context, performedBy bson.ObjectID, p Principal, in models.BulkStatusRequest) (*models.BulkActionResponse, error) {
	if n := len(in.AssetIDs); n == 0 {
		return nil, apperror.BadRequest("assetIds is required")
	} else if n > s.maxBulkAssets {
		return nil, apperror.BadRequest(fmt.Sprintf("batch exceeds MaxBulkAssets (%d)", s.maxBulkAssets))
	}

	attachmentIDs := derefAttachmentIDs(in.AttachmentIDs)
	if err := s.validateAttachments(ctx, attachmentIDs, performedBy); err != nil {
		return nil, err
	}

	lookup := func(id bson.ObjectID) (*models.Asset, error) {
		return s.assets.FindByID(ctx, id)
	}
	oks, results, allOK := validateBulkStatusRequest(in, p, lookup, s.maxBulkAssets)
	if !allOK {
		return s.bulkResponse(results), nil
	}

	sess, err := s.client.StartSession()
	if err != nil {
		return nil, apperror.Internal("start session", err)
	}
	defer sess.EndSession(ctx)

	_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		for _, v := range oks {
			if _, err := s.applyStatusChange(sc, v.asset, performedBy, models.StatusChangeRequest{
				Status: in.Status,
				Reason: in.Reason,
			}, attachmentIDs); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, apperror.Internal("bulk status transaction", err)
	}
	return s.bulkResponse(results), nil
}

// BulkUpdateCondition applies a condition change across many assets on a
// BEST-EFFORT basis: each successful item runs updateConditionForBulk — its
// own transaction, its own condition_change Movement — so the semantics are
// bit-identical to the single-asset path (UpdateCondition), aside from
// attachment validation, which runs once for the whole batch up front rather
// than per item. Failures are collected per-item (not_found / forbidden /
// unchanged); the batch does not abort. Global errors (invalid enum,
// empty/over-cap batch, bad attachments) are returned as 400 so the caller
// gets one shape when the whole request is malformed.
//
// Per-item loop is deliberate for now (each item needs its own from-condition
// capture, movement, and audit). If throughput becomes the bottleneck this
// can be rewritten with a single bulkWrite for the asset updates, but the
// movement + audit row must still be one-per-asset — do not collapse them.
func (s *AssetService) BulkUpdateCondition(ctx context.Context, performedBy bson.ObjectID, p Principal, in models.BulkConditionUpdate) (*models.BulkConditionResult, error) {
	lookup := func(id bson.ObjectID) (*models.Asset, error) {
		return s.assets.FindByID(ctx, id)
	}
	toUpdate, skipped, err := classifyBulkCondition(in, p, lookup, s.maxBulkAssets)
	if err != nil {
		return nil, err
	}

	// Validate the batch's shared attachment set exactly once, up front —
	// see updateConditionForBulk for why per-item revalidation would break.
	attachmentIDs := derefAttachmentIDs(in.AttachmentIDs)
	if err := s.validateAttachments(ctx, attachmentIDs, performedBy); err != nil {
		return nil, err
	}

	// Throwaway glue: classifyBulkCondition now returns the shared []bulkSkip;
	// this method's result type still speaks models.BulkConditionSkipped. This
	// method is dead (unrouted) and removed in a later task — convert here
	// rather than widen the public result type for a call site nothing hits.
	modelSkipped := make([]models.BulkConditionSkipped, 0, len(skipped))
	for _, sk := range skipped {
		modelSkipped = append(modelSkipped, models.BulkConditionSkipped{ID: sk.ID, Reason: sk.Reason})
	}

	out := &models.BulkConditionResult{Skipped: modelSkipped}
	single := models.ConditionUpdate{Condition: in.Condition, Notes: in.Notes}
	for _, id := range toUpdate {
		if _, err := s.updateConditionForBulk(ctx, id, performedBy, single, attachmentIDs); err != nil {
			// Race between plan and apply: something changed since our lookup.
			// Translate the two expected outcomes into skips; anything else is
			// a real error (Mongo/txn failure) and aborts the batch.
			switch {
			case isKind(err, apperror.KindConflict):
				out.Skipped = append(out.Skipped, models.BulkConditionSkipped{ID: id, Reason: "unchanged"})
			case isKind(err, apperror.KindNotFound):
				out.Skipped = append(out.Skipped, models.BulkConditionSkipped{ID: id, Reason: "not_found"})
			default:
				return nil, err
			}
			continue
		}
		out.Updated++
	}
	return out, nil
}

// classifyBulkCondition is the pure planner behind BulkUpdateCondition. It
// enforces the global 400 guards (enum, empty, cap), dedupes ids silently,
// and partitions the batch into (toUpdate, skipped). Extracted so the
// mixed-batch matrix can be table-tested without touching Mongo.
//
// A non-nil returned error is a global 400. Per-item outcomes are never
// returned as errors — they are recorded in skipped[].
func classifyBulkCondition(
	in models.BulkConditionUpdate,
	p Principal,
	lookup func(bson.ObjectID) (*models.Asset, error),
	maxBulk int,
) ([]bson.ObjectID, []bulkSkip, error) {
	if !validCondition(in.Condition) {
		return nil, nil, apperror.BadRequest("invalid condition")
	}
	if err := checkBatchSize(len(in.AssetIDs), maxBulk); err != nil {
		return nil, nil, err
	}
	return partitionBulk(in.AssetIDs, p, lookup, func(a *models.Asset) bool {
		return a.Condition == in.Condition // unchanged
	})
}

// bulkSkip is a per-asset skip recorded during planning: the row is counted
// (not written, not errored). Reason ∈ {not_found, forbidden, unchanged}.
// Package-internal — the live contract surfaces only the COUNT (job progress),
// never the list.
type bulkSkip struct {
	ID     bson.ObjectID
	Reason string
}

// planBulkTransfer partitions a transfer batch into (toUpdate, skipped) or
// returns a global 400. Mirrors classifyBulkCondition: request-level problems
// (empty/over-cap, dest venue forbidden/not-found) are 400s; per-asset problems
// (not_found, forbidden, same-venue no-op) are skips.
func planBulkTransfer(
	in models.BulkTransferRequest,
	p Principal,
	lookup func(bson.ObjectID) (*models.Asset, error),
	destExists bool,
	maxBulk int,
) ([]bson.ObjectID, []bulkSkip, error) {
	if err := checkBatchSize(len(in.AssetIDs), maxBulk); err != nil {
		return nil, nil, err
	}
	if !p.canAccessVenue(in.ToVenueID) {
		return nil, nil, apperror.BadRequest("toVenueId is outside your venue scope")
	}
	if !destExists {
		return nil, nil, apperror.BadRequest("toVenueId does not resolve to a known venue")
	}
	return partitionBulk(in.AssetIDs, p, lookup, func(a *models.Asset) (skip bool) {
		return a.CurrentVenueID == in.ToVenueID // same-venue transfer is a no-op
	})
}

// planBulkStatus partitions a status batch. A same-status row is an unchanged
// skip; a disallowed (non-no-op) transition is NOT rejected here — it flows to
// applyRow as an invalid_transition row error, exactly as an illegal condition
// change does for the reference endpoint.
func planBulkStatus(
	in models.BulkStatusRequest,
	p Principal,
	lookup func(bson.ObjectID) (*models.Asset, error),
	maxBulk int,
) ([]bson.ObjectID, []bulkSkip, error) {
	if err := checkBatchSize(len(in.AssetIDs), maxBulk); err != nil {
		return nil, nil, err
	}
	return partitionBulk(in.AssetIDs, p, lookup, func(a *models.Asset) bool {
		return a.Status == in.Status // no-op
	})
}

// planBulkAssign partitions an assign batch. A row already assigned to the
// target user is an unchanged skip. The target user's existence/active check is
// a request-level 400 handled by the caller (EnqueueAssign), not here.
func planBulkAssign(
	in models.BulkAssignRequest,
	p Principal,
	lookup func(bson.ObjectID) (*models.Asset, error),
	maxBulk int,
) ([]bson.ObjectID, []bulkSkip, error) {
	if err := checkBatchSize(len(in.AssetIDs), maxBulk); err != nil {
		return nil, nil, err
	}
	return partitionBulk(in.AssetIDs, p, lookup, func(a *models.Asset) bool {
		return a.ResponsibleUserID != nil && *a.ResponsibleUserID == in.ResponsibleUserID // no-op
	})
}

// checkBatchSize is the shared request-level batch guard.
func checkBatchSize(n, maxBulk int) error {
	if n == 0 {
		return apperror.BadRequest("assetIds is required")
	}
	if n > maxBulk {
		return apperror.BadRequest(fmt.Sprintf("batch exceeds MaxBulkAssets (%d)", maxBulk))
	}
	return nil
}

// partitionBulk is the shared per-asset planning loop: dedupe, look up, apply
// RBAC (forbidden skip) and a caller-supplied no-op predicate (unchanged skip),
// else queue for update. A lookup NotFound (or nil asset) is a not_found skip;
// any other lookup error is a global error.
func partitionBulk(
	ids []bson.ObjectID,
	p Principal,
	lookup func(bson.ObjectID) (*models.Asset, error),
	isNoOp func(*models.Asset) bool,
) ([]bson.ObjectID, []bulkSkip, error) {
	seen := make(map[bson.ObjectID]struct{}, len(ids))
	toUpdate := make([]bson.ObjectID, 0, len(ids))
	skipped := make([]bulkSkip, 0)
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}

		a, err := lookup(id)
		if err != nil {
			if isKind(err, apperror.KindNotFound) {
				skipped = append(skipped, bulkSkip{ID: id, Reason: "not_found"})
				continue
			}
			return nil, nil, err
		}
		if a == nil {
			skipped = append(skipped, bulkSkip{ID: id, Reason: "not_found"})
			continue
		}
		if !p.canAccessVenue(a.HomeVenueID) && !p.canAccessVenue(a.CurrentVenueID) {
			skipped = append(skipped, bulkSkip{ID: id, Reason: "forbidden"})
			continue
		}
		if isNoOp(a) {
			skipped = append(skipped, bulkSkip{ID: id, Reason: "unchanged"})
			continue
		}
		toUpdate = append(toUpdate, id)
	}
	return toUpdate, skipped, nil
}

// isKind reports whether err (or any error it wraps) is an *apperror.Error of
// the given kind. Lets the bulk path branch on expected per-item outcomes
// without stringly-typed comparisons.
func isKind(err error, k apperror.Kind) bool {
	var ae *apperror.Error
	if !errors.As(err, &ae) {
		return false
	}
	return ae.Kind == k
}

func (s *AssetService) bulkResponse(results []models.BulkActionResult) *models.BulkActionResponse {
	succ, fail := 0, 0
	for _, r := range results {
		if r.Ok {
			succ++
		} else {
			fail++
		}
	}
	return &models.BulkActionResponse{
		Total:     len(results),
		Succeeded: succ,
		Failed:    fail,
		Results:   results,
	}
}

func validateBulkAssignRequest(
	in models.BulkAssignRequest,
	p Principal,
	lookup func(bson.ObjectID) (*models.Asset, error),
	maxBulk int,
) ([]validatedAssign, []models.BulkActionResult, bool) {
	n := len(in.AssetIDs)
	if n == 0 || n > maxBulk {
		return nil, []models.BulkActionResult{{
			AssetID: bson.NilObjectID,
			Ok:      false,
			Error:   errString(fmt.Sprintf("batch size %d outside [1, %d]", n, maxBulk)),
		}}, false
	}

	results := make([]models.BulkActionResult, 0, n)
	seen := make(map[bson.ObjectID]struct{}, n)
	oks := make([]validatedAssign, 0, n)
	allOK := true
	for _, id := range in.AssetIDs {
		if _, dup := seen[id]; dup {
			results = append(results, models.BulkActionResult{AssetID: id, Ok: false, Error: errString("duplicate_id")})
			allOK = false
			continue
		}
		seen[id] = struct{}{}

		a, err := lookup(id)
		if err != nil || a == nil {
			results = append(results, models.BulkActionResult{AssetID: id, Ok: false, Error: errString("not_found")})
			allOK = false
			continue
		}
		if !p.canAccessVenue(a.HomeVenueID) && !p.canAccessVenue(a.CurrentVenueID) {
			results = append(results, models.BulkActionResult{AssetID: id, Ok: false, Error: errString("forbidden")})
			allOK = false
			continue
		}
		noOp := a.ResponsibleUserID != nil && *a.ResponsibleUserID == in.ResponsibleUserID
		results = append(results, models.BulkActionResult{AssetID: id, Ok: true})
		oks = append(oks, validatedAssign{asset: a, noOp: noOp})
	}
	return oks, results, allOK
}

// bulkAssignResponse tallies updated (noOp=false rows) vs skippedNoOp
// (noOp=true rows) from the validated set. On the failure path oks is nil
// and both counts are zero — the per-row results carry the diagnostics.
func bulkAssignResponse(oks []validatedAssign, results []models.BulkActionResult) *models.BulkAssignResponse {
	updated, skipped := 0, 0
	for _, v := range oks {
		if v.noOp {
			skipped++
		} else {
			updated++
		}
	}
	return &models.BulkAssignResponse{
		Total:       len(results),
		Updated:     updated,
		SkippedNoOp: skipped,
		Results:     results,
	}
}

// BulkAssign reassigns the responsibleUserId across up to MaxBulkAssets
// assets in a single Mongo transaction. Contract mirrors BulkTransfer:
// batch-level validation up-front, if any row fails the whole batch is
// returned with per-row diagnostics and no DB change is made. Assets
// already assigned to the target user are silently skipped as no-ops
// (counted in skippedNoOp; no movement written). One digest email is
// enqueued to the new custodian listing every asset actually updated —
// never one-per-asset, which would blow the outbound mail quota.
func (s *AssetService) BulkAssign(ctx context.Context, performedBy bson.ObjectID, p Principal, in models.BulkAssignRequest) (*models.BulkAssignResponse, error) {
	if n := len(in.AssetIDs); n == 0 {
		return nil, apperror.BadRequest("assetIds is required")
	} else if n > s.maxBulkAssets {
		return nil, apperror.BadRequest(fmt.Sprintf("batch exceeds MaxBulkAssets (%d)", s.maxBulkAssets))
	}

	// Shared for the batch — resolve once. Both "unknown" and "inactive"
	// collapse to 400 here: a bad target user is bad request input, not a
	// missing route resource, and the batch cannot proceed either way.
	u, err := s.users.FindByID(ctx, in.ResponsibleUserID)
	if err != nil {
		if isKind(err, apperror.KindNotFound) {
			return nil, apperror.BadRequest("responsibleUserId does not resolve to a known user")
		}
		return nil, err
	}
	if !u.IsActive {
		return nil, apperror.BadRequest("responsibleUserId is not an active user")
	}

	lookup := func(id bson.ObjectID) (*models.Asset, error) {
		return s.assets.FindByID(ctx, id)
	}
	oks, results, allOK := validateBulkAssignRequest(in, p, lookup, s.maxBulkAssets)
	if !allOK {
		return bulkAssignResponse(nil, results), nil
	}

	// Nothing to write? Skip the txn entirely, but still enqueue no digest —
	// there is nothing new for the custodian to hear about.
	updates := make([]validatedAssign, 0, len(oks))
	for _, v := range oks {
		if !v.noOp {
			updates = append(updates, v)
		}
	}

	attachmentIDs := derefAttachmentIDs(in.AttachmentIDs)
	if len(updates) > 0 {
		// Only validate attachments once we know we're actually going to
		// link them. Validating up-front (before the no-op filter) would
		// consume/inspect the caller's attachment upload even when every
		// row in the batch is a no-op and nothing is ever written — the
		// attachments would then sit at linked=false forever, an orphan
		// the caller has no way to explain (swept 24h later with no
		// error ever surfaced to them).
		if err := s.validateAttachments(ctx, attachmentIDs, performedBy); err != nil {
			return nil, err
		}

		sess, err := s.client.StartSession()
		if err != nil {
			return nil, apperror.Internal("start session", err)
		}
		defer sess.EndSession(ctx)

		_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
			for _, v := range updates {
				if _, err := s.applyAssignCustody(sc, v.asset, performedBy, in.ResponsibleUserID, in.Notes, attachmentIDs); err != nil {
					return nil, err
				}
			}
			return nil, nil
		})
		if err != nil {
			return nil, apperror.Internal("bulk assign transaction", err)
		}

		// Digest email fires AFTER the txn commits — outbox is a separate
		// collection, not part of the asset/movement atomic unit.
		refs := make([]notification.BulkCustodyAssignedRef, 0, len(updates))
		for _, v := range updates {
			refs = append(refs, notification.BulkCustodyAssignedRef{
				AssetID:   v.asset.ID,
				Tag:       v.asset.AssetTag,
				Name:      v.asset.Name,
				VenueName: venueLabelFor(ctx, s.venues, v.asset.CurrentVenueID),
				QRToken:   v.asset.QrToken,
			})
		}
		s.triggers.BulkCustodyAssignedDigest(ctx, in.ResponsibleUserID, refs)
	}

	return bulkAssignResponse(oks, results), nil
}

// venueLabelFor is a small in-service wrapper over the venue lookup: falls
// back to the hex ID when the venue is missing rather than error out (the
// asset-update txn already succeeded, we're only labelling for the email).
func venueLabelFor(ctx context.Context, venues *repository.VenueRepository, id bson.ObjectID) string {
	v, err := venues.FindByID(ctx, id)
	if err != nil || v == nil {
		return id.Hex()
	}
	return v.Name
}
