package service

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
	"imp/internal/pagination"
	"imp/internal/repository"
)

type ReportService struct {
	assets      *repository.AssetRepository
	venues      *repository.VenueRepository
	users       *repository.UserRepository
	repairs     *repository.RepairRepository
	departments *repository.DepartmentRepository
}

func NewReportService(
	assets *repository.AssetRepository,
	venues *repository.VenueRepository,
	users *repository.UserRepository,
	repairs *repository.RepairRepository,
	departments *repository.DepartmentRepository,
) *ReportService {
	return &ReportService{assets: assets, venues: venues, users: users, repairs: repairs, departments: departments}
}

// InventoryByVenue returns one row per home venue with total + per-status counts.
func (s *ReportService) InventoryByVenue(ctx context.Context) ([]models.InventoryByVenueRow, error) {
	inv, err := s.assets.AggregateInventoryByVenue(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]bson.ObjectID, 0, len(inv))
	for _, r := range inv {
		ids = append(ids, r.VenueID)
	}
	venues, err := s.venues.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	nameByID := make(map[bson.ObjectID]string, len(venues))
	for _, v := range venues {
		nameByID[v.ID] = v.Name
	}

	// Bulk fetch dept counts once (global aggregate), then partition by venue.
	deptCounts, err := s.assets.AggregateByDepartment(ctx, nil)
	if err != nil {
		return nil, err
	}
	deptIDs := make([]bson.ObjectID, 0, len(deptCounts))
	for _, dc := range deptCounts {
		deptIDs = append(deptIDs, dc.DepartmentID)
	}
	depts, err := s.departments.FindByIDs(ctx, deptIDs)
	if err != nil {
		return nil, err
	}
	deptNameByID := make(map[bson.ObjectID]string, len(depts))
	for _, d := range depts {
		deptNameByID[d.ID] = d.Name
	}
	byVenueDept := make(map[bson.ObjectID][]models.DepartmentAssetCountRow)
	for _, dc := range deptCounts {
		byVenueDept[dc.VenueID] = append(byVenueDept[dc.VenueID], models.DepartmentAssetCountRow{
			DepartmentID:   dc.DepartmentID,
			DepartmentName: deptNameByID[dc.DepartmentID],
			Count:          dc.Count,
		})
	}

	out := make([]models.InventoryByVenueRow, 0, len(inv))
	for _, r := range inv {
		byStatus := r.ByStatus
		row := models.InventoryByVenueRow{
			VenueID:   r.VenueID,
			VenueName: nameByID[r.VenueID],
			Total:     r.Total,
			ByStatus:  &byStatus,
		}
		if rows, ok := byVenueDept[r.VenueID]; ok && len(rows) > 0 {
			row.ByDepartment = &rows
		}
		out = append(out, row)
	}
	return out, nil
}

// ByDepartment returns one row per department with its asset count. When
// venueID is nil the aggregate spans all home venues.
func (s *ReportService) ByDepartment(ctx context.Context, venueID *bson.ObjectID) ([]models.DepartmentAssetCountRow, error) {
	counts, err := s.assets.AggregateByDepartment(ctx, venueID)
	if err != nil {
		return nil, err
	}
	ids := make([]bson.ObjectID, 0, len(counts))
	for _, c := range counts {
		ids = append(ids, c.DepartmentID)
	}
	depts, err := s.departments.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	nameByID := make(map[bson.ObjectID]string, len(depts))
	for _, d := range depts {
		nameByID[d.ID] = d.Name
	}
	out := make([]models.DepartmentAssetCountRow, 0, len(counts))
	for _, c := range counts {
		out = append(out, models.DepartmentAssetCountRow{
			DepartmentID:   c.DepartmentID,
			DepartmentName: nameByID[c.DepartmentID],
			Count:          c.Count,
		})
	}
	return out, nil
}

func (s *ReportService) AssetsAway(ctx context.Context, after *pagination.Cursor, limit int) ([]models.Asset, *pagination.Cursor, bool, error) {
	rows, hasMore, err := s.assets.FindAwayFromHomePage(ctx, after, limit)
	if err != nil {
		return nil, nil, false, err
	}
	return rows, nextAssetCursor(rows, hasMore), hasMore, nil
}

func (s *ReportService) AssetsOverdue(ctx context.Context, after *pagination.Cursor, limit int) ([]models.Asset, *pagination.Cursor, bool, error) {
	rows, hasMore, err := s.assets.FindOverduePage(ctx, after, limit)
	if err != nil {
		return nil, nil, false, err
	}
	return rows, nextAssetCursor(rows, hasMore), hasMore, nil
}

// InRepair returns repairs currently open or in_progress (i.e. not closed),
// keyset-paginated.
func (s *ReportService) InRepair(ctx context.Context, after *pagination.Cursor, limit int) ([]models.Repair, *pagination.Cursor, bool, error) {
	rows, hasMore, err := s.repairs.FindPage(ctx, bson.M{
		"status": bson.M{"$in": []models.RepairStatus{models.Open, models.InProgress}},
	}, after, limit)
	if err != nil {
		return nil, nil, false, err
	}
	var next *pagination.Cursor
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		next = &pagination.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return rows, next, hasMore, nil
}

// nextAssetCursor builds the cursor for the next page from the last asset in
// the current page, or nil when there is no next page.
func nextAssetCursor(rows []models.Asset, hasMore bool) *pagination.Cursor {
	if !hasMore || len(rows) == 0 {
		return nil
	}
	last := rows[len(rows)-1]
	return &pagination.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
}

// ByResponsible returns one row per custodian with their asset count.
func (s *ReportService) ByResponsible(ctx context.Context) ([]models.AssetsByResponsibleRow, error) {
	counts, err := s.assets.AggregateByResponsible(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]bson.ObjectID, 0, len(counts))
	for _, c := range counts {
		ids = append(ids, c.UserID)
	}
	users, err := s.users.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[bson.ObjectID]models.User, len(users))
	for _, u := range users {
		byID[u.ID] = u
	}
	out := make([]models.AssetsByResponsibleRow, 0, len(counts))
	for _, c := range counts {
		u, ok := byID[c.UserID]
		row := models.AssetsByResponsibleRow{UserID: c.UserID, Count: c.Count}
		if ok {
			row.UserName = u.Name
			if u.Position != "" {
				p := u.Position
				row.Position = &p
			}
		}
		out = append(out, row)
	}
	return out, nil
}
