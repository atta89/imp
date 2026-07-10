package service

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
)

// TestValidateAssetDepartment_HomeVenueChangeTriggersMismatch verifies the
// "reject on homeVenue change" branch via the real ValidateAssetDepartment
// helper (defined in department_integrity.go). Uses the shared fakeDeptFinder
// from department_integrity_test.go — same package.
func TestValidateAssetDepartment_HomeVenueChangeTriggersMismatch(t *testing.T) {
	oldHome := bson.NewObjectID()
	newHome := bson.NewObjectID()
	dept := &models.Department{ID: bson.NewObjectID(), VenueID: oldHome}

	f := &fakeDeptFinder{byID: map[bson.ObjectID]*models.Department{dept.ID: dept}}

	// Simulate what AssetService.Update does: the current departmentId is
	// unchanged in the request, but the new home venue is set. The service
	// re-validates against the new home venue, which mismatches → BadRequest.
	err := ValidateAssetDepartment(context.Background(), f, &dept.ID, newHome)
	appErr, ok := apperror.As(err)
	if !ok || appErr.Kind != apperror.KindBadRequest {
		t.Fatalf("want BadRequest on mismatch, got %v", err)
	}
	if appErr.Message != "department does not belong to the asset's home venue" {
		t.Errorf("unexpected message: %q", appErr.Message)
	}
}
