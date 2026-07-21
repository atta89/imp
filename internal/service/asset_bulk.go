package service

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/repository"
)

// MaxBulkAssets is the DEFAULT per-request asset cap shared by the bulk
// endpoints (async job enqueue and QR bulk validate). The effective cap is
// AssetService.maxBulkAssets, wired from BULK_MAX_ASSETS at construction;
// this const is the fallback used when config is zero and the value the pure
// planning helpers are exercised with in tests.
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

// classifyBulkCondition is the pure planner behind bulk condition updates. It
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
