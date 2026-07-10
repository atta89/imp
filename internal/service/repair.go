package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/notification"
	"imp/internal/repository"
)

type RepairService struct {
	repairs   *repository.RepairRepository
	assets    *repository.AssetRepository
	movements *repository.MovementRepository
	triggers  *notification.Triggers
}

func NewRepairService(
	repairs *repository.RepairRepository,
	assets *repository.AssetRepository,
	movements *repository.MovementRepository,
	triggers *notification.Triggers,
) *RepairService {
	return &RepairService{repairs: repairs, assets: assets, movements: movements, triggers: triggers}
}

type RepairListQuery struct {
	Status  models.RepairStatus
	AssetID *bson.ObjectID
}

func (s *RepairService) Create(ctx context.Context, in models.CreateRepairRequest, reportedBy bson.ObjectID) (*models.Repair, *models.Asset, error) {
	if strings.TrimSpace(in.Issue) == "" {
		return nil, nil, apperror.BadRequest("issue is required")
	}
	a, err := s.assets.FindByID(ctx, in.AssetID)
	if err != nil {
		return nil, nil, err
	}
	if !IsAllowedTransition(a.Status, models.InRepair) {
		return nil, nil, apperror.Conflict(fmt.Sprintf("cannot send asset in status %q to repair", a.Status))
	}

	now := time.Now().UTC()
	rep := &models.Repair{
		AssetID:    in.AssetID,
		Issue:      strings.TrimSpace(in.Issue),
		ReportedBy: reportedBy,
		ReportedAt: now,
		Status:     models.Open,
		Vendor:     in.Vendor,
		SentAt:     &now,
		Notes:      in.Notes,
	}
	if err := s.repairs.Create(ctx, rep); err != nil {
		return nil, nil, err
	}
	updated, err := s.assets.Update(ctx, in.AssetID, bson.M{"status": models.InRepair})
	if err != nil {
		return nil, nil, err
	}
	from, to := a.Status, models.InRepair
	if err := s.movements.Create(ctx, &models.Movement{
		AssetID:     in.AssetID,
		Type:        models.MovementTypeRepairIn,
		FromStatus:  &from,
		ToStatus:    &to,
		Reason:      &rep.Issue,
		PerformedBy: reportedBy,
	}); err != nil {
		return nil, nil, err
	}
	return rep, updated, nil
}

func (s *RepairService) Get(ctx context.Context, id bson.ObjectID) (*models.Repair, error) {
	return s.repairs.FindByID(ctx, id)
}

func (s *RepairService) List(ctx context.Context, q RepairListQuery, page, limit int) ([]models.Repair, int64, error) {
	filter := bson.M{}
	if q.Status != "" {
		filter["status"] = q.Status
	}
	if q.AssetID != nil {
		filter["assetId"] = *q.AssetID
	}
	return s.repairs.List(ctx, filter, page, limit)
}

// Update mutates the ticket and, if the status transition closes the ticket
// (completed | unrepairable), also transitions the asset (in_repair ->
// available | retired) and writes a repair_out movement.
func (s *RepairService) Update(ctx context.Context, id, performedBy bson.ObjectID, in models.UpdateRepairRequest) (*models.Repair, error) {
	existing, err := s.repairs.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if IsTerminalRepairStatus(existing.Status) {
		return nil, apperror.Conflict("repair is closed and read-only")
	}

	set := bson.M{}
	closesTo := models.RepairStatus("") // non-empty if this update closes the ticket
	if in.Status != nil {
		if !IsAllowedRepairTransition(existing.Status, *in.Status) {
			return nil, apperror.Conflict(fmt.Sprintf("cannot move repair from %q to %q", existing.Status, *in.Status))
		}
		set["status"] = *in.Status
		if IsTerminalRepairStatus(*in.Status) {
			closesTo = *in.Status
			now := time.Now().UTC()
			if in.ReturnedAt == nil {
				set["returnedAt"] = now
			}
		}
	}
	if in.Vendor != nil {
		set["vendor"] = *in.Vendor
	}
	if in.SentAt != nil {
		set["sentAt"] = *in.SentAt
	}
	if in.ReturnedAt != nil {
		set["returnedAt"] = *in.ReturnedAt
	}
	if in.Resolution != nil {
		set["resolution"] = *in.Resolution
	}
	if in.Notes != nil {
		set["notes"] = *in.Notes
	}

	updatedRep, err := s.repairs.Update(ctx, id, set)
	if err != nil {
		return nil, err
	}

	// If we just closed the ticket, transition the asset and log a repair_out
	// movement. Asset must currently be in_repair (the create flow set it);
	// if it's been moved elsewhere out-of-band (e.g. marked lost), we surface
	// that as a conflict — the repair is updated but the asset transition
	// is rejected by the state machine.
	if closesTo != "" {
		a, err := s.assets.FindByID(ctx, existing.AssetID)
		if err != nil {
			return nil, err
		}
		var toStatus models.AssetStatus
		if closesTo == models.Completed {
			toStatus = models.Available
		} else {
			toStatus = models.Retired
		}
		if !IsAllowedTransition(a.Status, toStatus) {
			return nil, apperror.Conflict(fmt.Sprintf("repair closed but asset is %q; expected in_repair", a.Status))
		}
		if _, err := s.assets.Update(ctx, existing.AssetID, bson.M{"status": toStatus}); err != nil {
			return nil, err
		}
		from := a.Status
		to := toStatus
		if err := s.movements.Create(ctx, &models.Movement{
			AssetID:     existing.AssetID,
			Type:        models.MovementTypeRepairOut,
			FromStatus:  &from,
			ToStatus:    &to,
			Reason:      updatedRep.Resolution,
			PerformedBy: performedBy,
		}); err != nil {
			return nil, err
		}
		// Re-read the asset so the trigger gets the post-transition view.
		updatedAsset, err := s.assets.FindByID(ctx, existing.AssetID)
		if err == nil {
			s.triggers.RepairClosed(ctx, updatedAsset, updatedRep)
		}
	}
	return updatedRep, nil
}
