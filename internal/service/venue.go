package service

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/repository"
)

type VenueService struct {
	venues      *repository.VenueRepository
	assets      *repository.AssetRepository
	departments *repository.DepartmentRepository
}

func NewVenueService(venues *repository.VenueRepository, assets *repository.AssetRepository, departments *repository.DepartmentRepository) *VenueService {
	return &VenueService{venues: venues, assets: assets, departments: departments}
}

func (s *VenueService) Create(ctx context.Context, in models.CreateVenueRequest) (*models.Venue, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, apperror.BadRequest("name is required")
	}
	if strings.TrimSpace(in.Code) == "" {
		return nil, apperror.BadRequest("code is required")
	}
	if strings.TrimSpace(in.Type) == "" {
		return nil, apperror.BadRequest("type is required")
	}
	v := &models.Venue{
		Name:     in.Name,
		Code:     in.Code,
		Type:     in.Type,
		Address:  in.Address,
		City:     in.City,
		IsActive: true,
	}
	if in.IsActive != nil {
		v.IsActive = *in.IsActive
	}
	if err := s.venues.Create(ctx, v); err != nil {
		return nil, err
	}
	return v, nil
}

func (s *VenueService) Get(ctx context.Context, id bson.ObjectID) (*models.Venue, error) {
	return s.venues.FindByID(ctx, id)
}

func (s *VenueService) List(ctx context.Context, page, limit int) ([]models.Venue, int64, error) {
	return s.venues.List(ctx, nil, page, limit)
}

// ListForUser scopes the listing to the venues the user has access to. Admins
// get everything; non-admins get only venues in their assigned scope.
func (s *VenueService) ListForUser(ctx context.Context, venueIDs []bson.ObjectID, page, limit int) ([]models.Venue, int64, error) {
	if venueIDs == nil {
		return s.venues.List(ctx, bson.M{"_id": bson.M{"$in": []bson.ObjectID{}}}, page, limit)
	}
	return s.venues.List(ctx, bson.M{"_id": bson.M{"$in": venueIDs}}, page, limit)
}

func (s *VenueService) Update(ctx context.Context, id bson.ObjectID, in models.UpdateVenueRequest) (*models.Venue, error) {
	set := bson.M{}
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return nil, apperror.BadRequest("name cannot be empty")
		}
		set["name"] = strings.TrimSpace(*in.Name)
	}
	if in.Code != nil {
		if strings.TrimSpace(*in.Code) == "" {
			return nil, apperror.BadRequest("code cannot be empty")
		}
		set["code"] = strings.TrimSpace(*in.Code)
	}
	if in.Type != nil {
		if strings.TrimSpace(*in.Type) == "" {
			return nil, apperror.BadRequest("type cannot be empty")
		}
		set["type"] = strings.TrimSpace(*in.Type)
	}
	if in.Address != nil {
		set["address"] = *in.Address
	}
	if in.City != nil {
		set["city"] = *in.City
	}
	if in.IsActive != nil {
		set["isActive"] = *in.IsActive
	}
	if len(set) == 0 {
		return s.venues.FindByID(ctx, id)
	}
	return s.venues.Update(ctx, id, set)
}

// Delete soft-blocks if any asset still calls this venue its home OR if any
// department still belongs to this venue (PRD §6.1).
func (s *VenueService) Delete(ctx context.Context, id bson.ObjectID) error {
	n, err := s.assets.CountByHomeVenue(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return apperror.Conflict("venue still has assets assigned as home venue; reassign them first")
	}
	m, err := s.departments.CountByVenue(ctx, id)
	if err != nil {
		return err
	}
	if m > 0 {
		return apperror.Conflict("venue still has departments; delete or reassign them first")
	}
	return s.venues.Delete(ctx, id)
}
