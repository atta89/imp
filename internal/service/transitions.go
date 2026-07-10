package service

import (
	"imp/internal/apperror"
	"imp/internal/models"
)

// validateConditionChange is the pure guard behind AssetService.UpdateCondition.
// Rejects unknown enum values (400) and no-op writes (409); returns nil when
// the move is legal. Separated so it can be table-tested without a repo.
func validateConditionChange(current, next models.AssetCondition) error {
	if !validCondition(next) {
		return apperror.BadRequest("invalid condition")
	}
	if current == next {
		return apperror.Conflict("asset condition is already " + string(next))
	}
	return nil
}

// statusTransitions encodes the asset state machine from PRD §5 and the
// allowed-transitions table in the original build prompt. Reading: a key
// (current status) maps to the set of statuses it is allowed to move to.
var statusTransitions = map[models.AssetStatus]map[models.AssetStatus]struct{}{
	models.Available: {
		models.InUse:    {}, // assign / deploy
		models.InRepair: {}, // report damage
		models.Retired:  {}, // dispose
		models.Lost:     {}, // any -> lost
	},
	models.InUse: {
		models.Available: {}, // return / unassign
		models.InRepair:  {}, // report damage
		models.Retired:   {}, // dispose
		models.Lost:      {}, // any -> lost
	},
	models.InRepair: {
		models.Available: {}, // repair done
		models.InUse:     {}, // repair done
		models.Retired:   {}, // unrepairable
		models.Lost:      {}, // any -> lost
	},
	models.Retired: {
		models.Lost: {}, // any -> lost (covers the rare "retired item also goes missing")
	},
	models.Lost: {
		models.Available: {}, // found
	},
}

// IsAllowedTransition reports whether moving from -> to is a legal asset
// status transition. Same-status moves are always rejected.
func IsAllowedTransition(from, to models.AssetStatus) bool {
	if from == to {
		return false
	}
	dests, ok := statusTransitions[from]
	if !ok {
		return false
	}
	_, ok = dests[to]
	return ok
}
