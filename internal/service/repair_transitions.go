package service

import "imp/internal/models"

// repairTransitions encodes the lifecycle of a repair ticket.
// open       -> in_progress (vendor takes the work)
// open       -> completed   (fixed without going to in_progress)
// open       -> unrepairable
// in_progress -> completed
// in_progress -> unrepairable
// completed / unrepairable are terminal.
var repairTransitions = map[models.RepairStatus]map[models.RepairStatus]struct{}{
	models.Open: {
		models.InProgress:   {},
		models.Completed:    {},
		models.Unrepairable: {},
	},
	models.InProgress: {
		models.Completed:    {},
		models.Unrepairable: {},
	},
}

// IsAllowedRepairTransition reports whether moving from -> to is a legal
// repair status transition. Same-status moves and moves out of a terminal
// state are rejected.
func IsAllowedRepairTransition(from, to models.RepairStatus) bool {
	if from == to {
		return false
	}
	dests, ok := repairTransitions[from]
	if !ok {
		return false
	}
	_, ok = dests[to]
	return ok
}

// IsTerminalRepairStatus returns true once the ticket can't be moved further.
func IsTerminalRepairStatus(s models.RepairStatus) bool {
	switch s {
	case models.Completed, models.Unrepairable:
		return true
	}
	return false
}
