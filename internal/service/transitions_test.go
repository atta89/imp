package service

import (
	"errors"
	"testing"

	"imp/internal/apperror"
	"imp/internal/models"
)

func TestIsAllowedTransition(t *testing.T) {
	all := []models.AssetStatus{
		models.Available,
		models.InUse,
		models.InRepair,
		models.Retired,
		models.Lost,
	}

	// The full set of allowed transitions per PRD §5. Anything not in this
	// set must be rejected, including all same-status pairs.
	allowed := map[[2]models.AssetStatus]struct{}{
		{models.Available, models.InUse}:    {},
		{models.Available, models.InRepair}: {},
		{models.Available, models.Retired}:  {},
		{models.Available, models.Lost}:     {},

		{models.InUse, models.Available}: {},
		{models.InUse, models.InRepair}:  {},
		{models.InUse, models.Retired}:   {},
		{models.InUse, models.Lost}:      {},

		{models.InRepair, models.Available}: {},
		{models.InRepair, models.InUse}:     {},
		{models.InRepair, models.Retired}:   {},
		{models.InRepair, models.Lost}:      {},

		{models.Retired, models.Lost}: {},

		{models.Lost, models.Available}: {},
	}

	for _, from := range all {
		for _, to := range all {
			_, want := allowed[[2]models.AssetStatus{from, to}]
			got := IsAllowedTransition(from, to)
			if got != want {
				t.Errorf("IsAllowedTransition(%q -> %q): got %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestIsAllowedTransition_SameStatusRejected(t *testing.T) {
	all := []models.AssetStatus{
		models.Available, models.InUse, models.InRepair, models.Retired, models.Lost,
	}
	for _, s := range all {
		if IsAllowedTransition(s, s) {
			t.Errorf("IsAllowedTransition(%q -> %q) should be false (no-op)", s, s)
		}
	}
}

func TestIsAllowedTransition_UnknownStatusRejected(t *testing.T) {
	bogus := models.AssetStatus("not_a_real_status")
	if IsAllowedTransition(bogus, models.Available) {
		t.Error("transition from unknown status must be rejected")
	}
	if IsAllowedTransition(models.Available, bogus) {
		t.Error("transition to unknown status must be rejected")
	}
}

// validateConditionChange is the pure guard behind AssetService.UpdateCondition.
// It doesn't touch state, so its full behavior is exercised here; the DB-write
// and RBAC paths follow the sibling-action precedent (untested — no fake infra).
func TestValidateConditionChange(t *testing.T) {
	cases := []struct {
		name       string
		current    models.AssetCondition
		next       models.AssetCondition
		wantKind   apperror.Kind // "" means expect nil
	}{
		{"good->fair is a legal change", models.Good, models.Fair, ""},
		{"new->poor is a legal change", models.New, models.Poor, ""},
		{"unknown enum rejected as bad request", models.Good, models.AssetCondition("mint"), apperror.KindBadRequest},
		{"empty enum rejected as bad request", models.Good, models.AssetCondition(""), apperror.KindBadRequest},
		{"unchanged value rejected as conflict", models.Fair, models.Fair, apperror.KindConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConditionChange(tc.current, tc.next)
			if tc.wantKind == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			var ae *apperror.Error
			if !errors.As(err, &ae) {
				t.Fatalf("want *apperror.Error, got %T: %v", err, err)
			}
			if ae.Kind != tc.wantKind {
				t.Errorf("kind: want %s, got %s", tc.wantKind, ae.Kind)
			}
		})
	}
}
