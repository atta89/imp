package service

import (
	"testing"

	"imp/internal/models"
)

func TestIsAllowedRepairTransition(t *testing.T) {
	all := []models.RepairStatus{
		models.Open, models.InProgress, models.Completed, models.Unrepairable,
	}

	allowed := map[[2]models.RepairStatus]struct{}{
		{models.Open, models.InProgress}:        {},
		{models.Open, models.Completed}:         {},
		{models.Open, models.Unrepairable}:      {},
		{models.InProgress, models.Completed}:   {},
		{models.InProgress, models.Unrepairable}: {},
	}

	for _, from := range all {
		for _, to := range all {
			_, want := allowed[[2]models.RepairStatus{from, to}]
			got := IsAllowedRepairTransition(from, to)
			if got != want {
				t.Errorf("IsAllowedRepairTransition(%q -> %q): got %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestIsTerminalRepairStatus(t *testing.T) {
	cases := []struct {
		s    models.RepairStatus
		want bool
	}{
		{models.Open, false},
		{models.InProgress, false},
		{models.Completed, true},
		{models.Unrepairable, true},
	}
	for _, c := range cases {
		if got := IsTerminalRepairStatus(c.s); got != c.want {
			t.Errorf("IsTerminalRepairStatus(%q): got %v, want %v", c.s, got, c.want)
		}
	}
}
