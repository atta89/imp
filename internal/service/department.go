package service

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/repository"
)

type DepartmentService struct {
	depts  *repository.DepartmentRepository
	assets *repository.AssetRepository
	venues *repository.VenueRepository
}

func NewDepartmentService(depts *repository.DepartmentRepository, assets *repository.AssetRepository, venues *repository.VenueRepository) *DepartmentService {
	return &DepartmentService{depts: depts, assets: assets, venues: venues}
}

func (s *DepartmentService) Create(ctx context.Context, venueID bson.ObjectID, in models.CreateDepartmentRequest) (*models.Department, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, apperror.BadRequest("name is required")
	}
	if strings.TrimSpace(in.Code) == "" {
		return nil, apperror.BadRequest("code is required")
	}
	if _, err := s.venues.FindByID(ctx, venueID); err != nil {
		return nil, err
	}
	d := &models.Department{
		VenueID:     venueID,
		Name:        in.Name,
		Code:        in.Code,
		Description: in.Description,
		IsActive:    true,
	}
	if in.IsActive != nil {
		d.IsActive = *in.IsActive
	}
	if err := s.depts.Create(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// Get returns a department by id, but only if it belongs to the parent
// venueID. Any mismatch surfaces as NotFound so scoping cannot leak the
// existence of departments in other venues.
func (s *DepartmentService) Get(ctx context.Context, venueID, id bson.ObjectID) (*models.Department, error) {
	d, err := s.depts.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.VenueID != venueID {
		return nil, apperror.NotFound("department not found")
	}
	return d, nil
}

func (s *DepartmentService) List(ctx context.Context, venueID bson.ObjectID, page, limit int) ([]models.Department, int64, error) {
	return s.depts.ListByVenue(ctx, venueID, page, limit)
}

func (s *DepartmentService) Update(ctx context.Context, venueID, id bson.ObjectID, in models.UpdateDepartmentRequest) (*models.Department, error) {
	if _, err := s.Get(ctx, venueID, id); err != nil {
		return nil, err
	}
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
	if in.Description != nil {
		set["description"] = *in.Description
	}
	if in.IsActive != nil {
		set["isActive"] = *in.IsActive
	}
	if len(set) == 0 {
		return s.depts.FindByID(ctx, id)
	}
	return s.depts.Update(ctx, id, set)
}

// Delete soft-blocks if any asset references this department. Matches the
// venue/category/user soft-block policy.
func (s *DepartmentService) Delete(ctx context.Context, venueID, id bson.ObjectID) error {
	if _, err := s.Get(ctx, venueID, id); err != nil {
		return err
	}
	n, err := s.assets.CountByDepartment(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return apperror.Conflict("department is still used by assets; reassign them first")
	}
	return s.depts.Delete(ctx, id)
}
