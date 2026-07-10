package service

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
	"imp/internal/repository"
)

type DashboardService struct {
	assets *repository.AssetRepository
	venues *repository.VenueRepository
}

func NewDashboardService(assets *repository.AssetRepository, venues *repository.VenueRepository) *DashboardService {
	return &DashboardService{assets: assets, venues: venues}
}

func (s *DashboardService) Summary(ctx context.Context) (*models.DashboardSummary, error) {
	total, err := s.assets.CountAll(ctx)
	if err != nil {
		return nil, err
	}
	byStatus := map[string]int{}
	for _, st := range []models.AssetStatus{models.Available, models.InUse, models.InRepair, models.Retired, models.Lost} {
		n, err := s.assets.CountByStatusValue(ctx, st)
		if err != nil {
			return nil, err
		}
		byStatus[string(st)] = int(n)
	}
	away, err := s.assets.CountAwayFromHome(ctx)
	if err != nil {
		return nil, err
	}
	overdue, err := s.assets.CountOverdue(ctx)
	if err != nil {
		return nil, err
	}

	venueCounts, err := s.assets.AggregateByHomeVenue(ctx)
	if err != nil {
		return nil, err
	}
	byVenue, err := s.hydrateVenueCounts(ctx, venueCounts)
	if err != nil {
		return nil, err
	}

	return &models.DashboardSummary{
		TotalAssets:  int(total),
		ByStatus:     byStatus,
		ByVenue:      byVenue,
		AwayFromHome: int(away),
		InRepair:     byStatus[string(models.InRepair)],
		Overdue:      int(overdue),
	}, nil
}

func (s *DashboardService) hydrateVenueCounts(ctx context.Context, counts []repository.VenueCount) ([]models.DashboardVenueCount, error) {
	ids := make([]bson.ObjectID, 0, len(counts))
	for _, c := range counts {
		ids = append(ids, c.VenueID)
	}
	venues, err := s.venues.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	nameByID := make(map[bson.ObjectID]string, len(venues))
	for _, v := range venues {
		nameByID[v.ID] = v.Name
	}
	out := make([]models.DashboardVenueCount, 0, len(counts))
	for _, c := range counts {
		out = append(out, models.DashboardVenueCount{
			VenueID:   c.VenueID,
			VenueName: nameByID[c.VenueID],
			Count:     c.Count,
		})
	}
	return out, nil
}
