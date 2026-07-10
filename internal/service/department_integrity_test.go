package service

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
)

// fakeDeptFinder is an in-memory DepartmentFinder used across department tests.
type fakeDeptFinder struct {
	byID map[bson.ObjectID]*models.Department
}

func (f *fakeDeptFinder) FindByID(_ context.Context, id bson.ObjectID) (*models.Department, error) {
	d, ok := f.byID[id]
	if !ok {
		return nil, apperror.NotFound("department not found")
	}
	return d, nil
}

func TestValidateAssetDepartment_NilPasses(t *testing.T) {
	if err := ValidateAssetDepartment(context.Background(), &fakeDeptFinder{}, nil, bson.NewObjectID()); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestValidateAssetDepartment_UnknownIsBadRequest(t *testing.T) {
	f := &fakeDeptFinder{byID: map[bson.ObjectID]*models.Department{}}
	id := bson.NewObjectID()
	err := ValidateAssetDepartment(context.Background(), f, &id, bson.NewObjectID())
	appErr, ok := apperror.As(err)
	if !ok || appErr.Kind != apperror.KindBadRequest {
		t.Fatalf("want BadRequest, got %v", err)
	}
	if appErr.Message != "department not found" {
		t.Errorf("message: want %q, got %q", "department not found", appErr.Message)
	}
}

func TestValidateAssetDepartment_MismatchIsBadRequest(t *testing.T) {
	venueA := bson.NewObjectID()
	venueB := bson.NewObjectID()
	deptID := bson.NewObjectID()
	f := &fakeDeptFinder{byID: map[bson.ObjectID]*models.Department{
		deptID: {ID: deptID, VenueID: venueA},
	}}
	err := ValidateAssetDepartment(context.Background(), f, &deptID, venueB)
	appErr, ok := apperror.As(err)
	if !ok || appErr.Kind != apperror.KindBadRequest {
		t.Fatalf("want BadRequest, got %v", err)
	}
	if appErr.Message != "department does not belong to the asset's home venue" {
		t.Errorf("mismatch message unexpected: %q", appErr.Message)
	}
}

func TestValidateAssetDepartment_MatchPasses(t *testing.T) {
	venue := bson.NewObjectID()
	deptID := bson.NewObjectID()
	f := &fakeDeptFinder{byID: map[bson.ObjectID]*models.Department{
		deptID: {ID: deptID, VenueID: venue},
	}}
	if err := ValidateAssetDepartment(context.Background(), f, &deptID, venue); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}
