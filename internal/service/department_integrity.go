package service

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/repository"
)

// DepartmentFinder is the minimal contract ValidateAssetDepartment needs. The
// concrete *repository.DepartmentRepository satisfies it; tests use fakes.
type DepartmentFinder interface {
	FindByID(ctx context.Context, id bson.ObjectID) (*models.Department, error)
}

// ValidateAssetDepartment enforces the invariant:
//   asset.departmentId == nil  OR
//   department.venueId == asset.homeVenueId
// Called from every path that sets an asset's departmentId — Create, Update
// (when either departmentId or homeVenueId is changing), /assign, PO receive,
// and the bulk-import commit path.
func ValidateAssetDepartment(ctx context.Context, depts DepartmentFinder, deptID *bson.ObjectID, homeVenueID bson.ObjectID) error {
	if deptID == nil {
		return nil
	}
	d, err := depts.FindByID(ctx, *deptID)
	if err != nil {
		if appErr, ok := apperror.As(err); ok && appErr.Kind == apperror.KindNotFound {
			return apperror.BadRequest("department not found")
		}
		return err
	}
	if d.VenueID != homeVenueID {
		return apperror.BadRequest("department does not belong to the asset's home venue")
	}
	return nil
}

// Compile-time proof that *repository.DepartmentRepository satisfies
// DepartmentFinder. If the repo's FindByID signature ever drifts, the build
// fails here (not just in the tests).
var _ DepartmentFinder = (*repository.DepartmentRepository)(nil)
