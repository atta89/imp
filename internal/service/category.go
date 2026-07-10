package service

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/repository"
)

type CategoryService struct {
	categories *repository.CategoryRepository
	assets     *repository.AssetRepository
}

func NewCategoryService(categories *repository.CategoryRepository, assets *repository.AssetRepository) *CategoryService {
	return &CategoryService{categories: categories, assets: assets}
}

func (s *CategoryService) Create(ctx context.Context, in models.CreateCategoryRequest) (*models.Category, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, apperror.BadRequest("name is required")
	}
	if strings.TrimSpace(in.Slug) == "" {
		return nil, apperror.BadRequest("slug is required")
	}
	c := &models.Category{
		Name:        in.Name,
		Slug:        in.Slug,
		Description: in.Description,
		IsActive:    true,
	}
	if in.IsActive != nil {
		c.IsActive = *in.IsActive
	}
	if in.CustomFields != nil {
		c.CustomFields = *in.CustomFields
	}
	if err := s.categories.Create(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *CategoryService) Get(ctx context.Context, id bson.ObjectID) (*models.Category, error) {
	return s.categories.FindByID(ctx, id)
}

func (s *CategoryService) List(ctx context.Context, page, limit int) ([]models.Category, int64, error) {
	return s.categories.List(ctx, page, limit)
}

func (s *CategoryService) Update(ctx context.Context, id bson.ObjectID, in models.UpdateCategoryRequest) (*models.Category, error) {
	set := bson.M{}
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return nil, apperror.BadRequest("name cannot be empty")
		}
		set["name"] = strings.TrimSpace(*in.Name)
	}
	if in.Slug != nil {
		if strings.TrimSpace(*in.Slug) == "" {
			return nil, apperror.BadRequest("slug cannot be empty")
		}
		set["slug"] = strings.ToLower(strings.TrimSpace(*in.Slug))
	}
	if in.Description != nil {
		set["description"] = *in.Description
	}
	if in.CustomFields != nil {
		set["customFields"] = *in.CustomFields
	}
	if in.IsActive != nil {
		set["isActive"] = *in.IsActive
	}
	if len(set) == 0 {
		return s.categories.FindByID(ctx, id)
	}
	return s.categories.Update(ctx, id, set)
}

// Delete soft-blocks if any asset still uses this category.
func (s *CategoryService) Delete(ctx context.Context, id bson.ObjectID) error {
	n, err := s.assets.CountByCategory(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return apperror.Conflict("category is still used by assets; reassign them first")
	}
	return s.categories.Delete(ctx, id)
}
