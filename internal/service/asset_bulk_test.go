package service

import (
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
)

func okPrincipal(venues ...bson.ObjectID) Principal {
	set := make(map[string]struct{}, len(venues))
	for _, v := range venues {
		set[v.Hex()] = struct{}{}
	}
	return Principal{VenueIDs: set}
}

// mkAssetC builds a minimal asset with a condition set — the bulk-condition
// planner keys off Condition + Home/CurrentVenueID, not Status.
func mkAssetC(id, home, current bson.ObjectID, cond models.AssetCondition) *models.Asset {
	return &models.Asset{
		ID:             id,
		HomeVenueID:    home,
		CurrentVenueID: current,
		Condition:      cond,
	}
}

// storeLookup returns a `lookup` closure that mimics assets.FindByID: known ids
// resolve to the stored asset, unknown ids return apperror.NotFound (which the
// planner translates to a "not_found" skip).
func storeLookup(store map[bson.ObjectID]*models.Asset) func(bson.ObjectID) (*models.Asset, error) {
	return func(id bson.ObjectID) (*models.Asset, error) {
		if a, ok := store[id]; ok {
			return a, nil
		}
		return nil, apperror.NotFound("asset not found")
	}
}

// skippedByID rebuilds a {id: reason} map so tests can assert without
// depending on the traversal order of in.AssetIDs (which the planner does
// preserve, but tests read more clearly when keyed by id).
func skippedByID(sk []bulkSkip) map[bson.ObjectID]string {
	m := make(map[bson.ObjectID]string, len(sk))
	for _, s := range sk {
		m[s.ID] = s.Reason
	}
	return m
}

func TestClassifyBulkCondition_MixedBatch(t *testing.T) {
	inScope := bson.NewObjectID()
	outOfScope := bson.NewObjectID()

	aUpdate := bson.NewObjectID()    // Good -> Fair: planned
	aUnchanged := bson.NewObjectID() // Fair -> Fair: skip unchanged
	aNotFound := bson.NewObjectID()  // no store entry: skip not_found
	aForbidden := bson.NewObjectID() // out-of-scope venue: skip forbidden
	store := map[bson.ObjectID]*models.Asset{
		aUpdate:    mkAssetC(aUpdate, inScope, inScope, models.Good),
		aUnchanged: mkAssetC(aUnchanged, inScope, inScope, models.Fair),
		aForbidden: mkAssetC(aForbidden, outOfScope, outOfScope, models.Good),
	}
	in := models.BulkConditionUpdate{
		AssetIDs:  []bson.ObjectID{aUpdate, aUnchanged, aNotFound, aForbidden},
		Condition: models.Fair,
	}

	toUpdate, skipped, err := classifyBulkCondition(in, okPrincipal(inScope), storeLookup(store), MaxBulkAssets)
	if err != nil {
		t.Fatalf("planner errored: %v", err)
	}
	if len(toUpdate) != 1 || toUpdate[0] != aUpdate {
		t.Errorf("toUpdate: want [%s]; got %v", aUpdate.Hex(), toUpdate)
	}
	byID := skippedByID(skipped)
	wantSkip := map[bson.ObjectID]string{
		aUnchanged: "unchanged",
		aNotFound:  "not_found",
		aForbidden: "forbidden",
	}
	for id, want := range wantSkip {
		if byID[id] != want {
			t.Errorf("skip[%s]: want %q; got %q", id.Hex(), want, byID[id])
		}
	}
	if len(skipped) != 3 {
		t.Errorf("expected 3 skips; got %d (%+v)", len(skipped), skipped)
	}
}

func TestClassifyBulkCondition_DedupSilent(t *testing.T) {
	home := bson.NewObjectID()
	a := bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{
		a: mkAssetC(a, home, home, models.Good),
	}
	in := models.BulkConditionUpdate{
		AssetIDs:  []bson.ObjectID{a, a, a},
		Condition: models.Fair,
	}
	toUpdate, skipped, err := classifyBulkCondition(in, okPrincipal(home), storeLookup(store), MaxBulkAssets)
	if err != nil {
		t.Fatalf("planner errored: %v", err)
	}
	if len(toUpdate) != 1 {
		t.Errorf("duplicates should collapse; got toUpdate=%v", toUpdate)
	}
	if len(skipped) != 0 {
		t.Errorf("duplicates should be silent, not skipped; got %+v", skipped)
	}
}

func TestClassifyBulkCondition_GlobalErrors(t *testing.T) {
	home := bson.NewObjectID()
	oneID := []bson.ObjectID{bson.NewObjectID()}

	overCap := make([]bson.ObjectID, MaxBulkAssets+1)
	for i := range overCap {
		overCap[i] = bson.NewObjectID()
	}

	cases := []struct {
		name string
		in   models.BulkConditionUpdate
	}{
		{"empty batch", models.BulkConditionUpdate{AssetIDs: nil, Condition: models.Good}},
		{"invalid enum", models.BulkConditionUpdate{AssetIDs: oneID, Condition: models.AssetCondition("mint")}},
		{"empty enum", models.BulkConditionUpdate{AssetIDs: oneID, Condition: models.AssetCondition("")}},
		{"over cap", models.BulkConditionUpdate{AssetIDs: overCap, Condition: models.Good}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := classifyBulkCondition(tc.in, okPrincipal(home), storeLookup(nil), MaxBulkAssets)
			var ae *apperror.Error
			if !errors.As(err, &ae) {
				t.Fatalf("want *apperror.Error, got %T: %v", err, err)
			}
			if ae.Kind != apperror.KindBadRequest {
				t.Errorf("kind: want %s, got %s", apperror.KindBadRequest, ae.Kind)
			}
		})
	}
}

func TestClassifyBulkCondition_AdminBypassesVenueScope(t *testing.T) {
	// Admin has no venue scope; every asset should be planned regardless of
	// which venue owns it. Regression guard against accidentally treating
	// isAdmin=true as "no scope = no access".
	v1, v2 := bson.NewObjectID(), bson.NewObjectID()
	a, b := bson.NewObjectID(), bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{
		a: mkAssetC(a, v1, v1, models.Good),
		b: mkAssetC(b, v2, v2, models.Good),
	}
	in := models.BulkConditionUpdate{
		AssetIDs:  []bson.ObjectID{a, b},
		Condition: models.Fair,
	}
	toUpdate, skipped, err := classifyBulkCondition(in, Principal{IsAdmin: true}, storeLookup(store), MaxBulkAssets)
	if err != nil {
		t.Fatalf("planner errored: %v", err)
	}
	if len(toUpdate) != 2 {
		t.Errorf("admin should reach both assets; got toUpdate=%v skipped=%+v", toUpdate, skipped)
	}
}

func TestIsKind(t *testing.T) {
	if !isKind(apperror.BadRequest("x"), apperror.KindBadRequest) {
		t.Error("BadRequest should match KindBadRequest")
	}
	if isKind(apperror.BadRequest("x"), apperror.KindConflict) {
		t.Error("BadRequest should not match KindConflict")
	}
	if isKind(errors.New("plain"), apperror.KindBadRequest) {
		t.Error("plain error should not match any kind")
	}
	if isKind(nil, apperror.KindBadRequest) {
		t.Error("nil should not match any kind")
	}
}

func TestBuildCustodyMovement_HasCorrectFromToAndType(t *testing.T) {
	oldU := bson.NewObjectID()
	newU := bson.NewObjectID()
	performedBy := bson.NewObjectID()
	assetID := bson.NewObjectID()
	notes := "handover"
	a := &models.Asset{ID: assetID, ResponsibleUserID: &oldU}

	m := buildCustodyMovement(a, performedBy, newU, &notes)

	if m.Type != models.MovementTypeCustodyChange {
		t.Errorf("Type: want custody_change; got %s", m.Type)
	}
	if m.AssetID != assetID {
		t.Errorf("AssetID: want %s; got %s", assetID.Hex(), m.AssetID.Hex())
	}
	if m.FromUserID == nil || *m.FromUserID != oldU {
		t.Errorf("FromUserID: want %s; got %+v", oldU.Hex(), m.FromUserID)
	}
	if m.ToUserID == nil || *m.ToUserID != newU {
		t.Errorf("ToUserID: want %s; got %+v", newU.Hex(), m.ToUserID)
	}
	if m.PerformedBy != performedBy {
		t.Errorf("PerformedBy: want %s; got %s", performedBy.Hex(), m.PerformedBy.Hex())
	}
	if m.Notes == nil || *m.Notes != notes {
		t.Errorf("Notes: want %q; got %+v", notes, m.Notes)
	}
}

func TestBuildCustodyMovement_NilFromWhenNoPriorCustodian(t *testing.T) {
	newU := bson.NewObjectID()
	a := &models.Asset{ID: bson.NewObjectID(), ResponsibleUserID: nil}
	m := buildCustodyMovement(a, bson.NewObjectID(), newU, nil)
	if m.FromUserID != nil {
		t.Errorf("FromUserID: want nil; got %+v", m.FromUserID)
	}
}

func TestPlanBulkTransfer_Partitions(t *testing.T) {
	venueA := bson.NewObjectID() // in scope
	venueB := bson.NewObjectID() // dest, in scope
	inScope := Principal{VenueIDs: map[string]struct{}{venueA.Hex(): {}, venueB.Hex(): {}}}

	moveable := &models.Asset{ID: bson.NewObjectID(), HomeVenueID: venueA, CurrentVenueID: venueA}
	sameVenue := &models.Asset{ID: bson.NewObjectID(), HomeVenueID: venueB, CurrentVenueID: venueB}
	outOfScope := &models.Asset{ID: bson.NewObjectID(), HomeVenueID: bson.NewObjectID(), CurrentVenueID: bson.NewObjectID()}
	missingID := bson.NewObjectID()

	byID := map[bson.ObjectID]*models.Asset{moveable.ID: moveable, sameVenue.ID: sameVenue, outOfScope.ID: outOfScope}
	lookup := func(id bson.ObjectID) (*models.Asset, error) {
		if a, ok := byID[id]; ok {
			return a, nil
		}
		return nil, apperror.NotFound("asset not found")
	}

	in := models.BulkTransferRequest{
		AssetIDs:  []bson.ObjectID{moveable.ID, sameVenue.ID, outOfScope.ID, missingID, moveable.ID},
		ToVenueID: venueB,
	}
	toUpdate, skipped, err := planBulkTransfer(in, inScope, lookup, true, 5000)
	if err != nil {
		t.Fatalf("unexpected global error: %v", err)
	}
	if len(toUpdate) != 1 || toUpdate[0] != moveable.ID {
		t.Fatalf("toUpdate = %v, want [%s]", toUpdate, moveable.ID.Hex())
	}
	got := map[string]string{}
	for _, s := range skipped {
		got[s.ID.Hex()] = s.Reason
	}
	if got[sameVenue.ID.Hex()] != "unchanged" {
		t.Errorf("sameVenue reason = %q, want unchanged", got[sameVenue.ID.Hex()])
	}
	if got[outOfScope.ID.Hex()] != "forbidden" {
		t.Errorf("outOfScope reason = %q, want forbidden", got[outOfScope.ID.Hex()])
	}
	if got[missingID.Hex()] != "not_found" {
		t.Errorf("missing reason = %q, want not_found", got[missingID.Hex()])
	}
	if _, dup := got[moveable.ID.Hex()]; dup {
		t.Errorf("duplicate moveable id should not be skipped, just deduped")
	}
}

func TestPlanBulkTransfer_GlobalErrors(t *testing.T) {
	p := Principal{IsAdmin: true}
	lookup := func(id bson.ObjectID) (*models.Asset, error) { return nil, apperror.NotFound("x") }

	if _, _, err := planBulkTransfer(models.BulkTransferRequest{AssetIDs: nil, ToVenueID: bson.NewObjectID()}, p, lookup, true, 5000); !isKind(err, apperror.KindBadRequest) {
		t.Errorf("empty batch: want BadRequest, got %v", err)
	}
	ids := make([]bson.ObjectID, 3)
	if _, _, err := planBulkTransfer(models.BulkTransferRequest{AssetIDs: ids, ToVenueID: bson.NewObjectID()}, p, lookup, true, 2); !isKind(err, apperror.KindBadRequest) {
		t.Errorf("over cap: want BadRequest, got %v", err)
	}
	if _, _, err := planBulkTransfer(models.BulkTransferRequest{AssetIDs: ids, ToVenueID: bson.NewObjectID()}, p, lookup, false, 5000); !isKind(err, apperror.KindBadRequest) {
		t.Errorf("dest not found: want BadRequest, got %v", err)
	}
	nonAdmin := Principal{VenueIDs: map[string]struct{}{}}
	if _, _, err := planBulkTransfer(models.BulkTransferRequest{AssetIDs: ids, ToVenueID: bson.NewObjectID()}, nonAdmin, lookup, true, 5000); !isKind(err, apperror.KindBadRequest) {
		t.Errorf("dest forbidden: want BadRequest, got %v", err)
	}
}

func TestPlanBulkStatus_NoOpAndForbidden(t *testing.T) {
	venue := bson.NewObjectID()
	p := Principal{VenueIDs: map[string]struct{}{venue.Hex(): {}}}
	same := &models.Asset{ID: bson.NewObjectID(), HomeVenueID: venue, CurrentVenueID: venue, Status: models.Available}
	diff := &models.Asset{ID: bson.NewObjectID(), HomeVenueID: venue, CurrentVenueID: venue, Status: models.InUse}
	byID := map[bson.ObjectID]*models.Asset{same.ID: same, diff.ID: diff}
	lookup := func(id bson.ObjectID) (*models.Asset, error) {
		if a, ok := byID[id]; ok {
			return a, nil
		}
		return nil, apperror.NotFound("nf")
	}
	in := models.BulkStatusRequest{AssetIDs: []bson.ObjectID{same.ID, diff.ID}, Status: models.Available}
	toUpdate, skipped, err := planBulkStatus(in, p, lookup, 5000)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(toUpdate) != 1 || toUpdate[0] != diff.ID {
		t.Fatalf("toUpdate = %v, want [%s]", toUpdate, diff.ID.Hex())
	}
	if len(skipped) != 1 || skipped[0].ID != same.ID || skipped[0].Reason != "unchanged" {
		t.Fatalf("skipped = %+v, want [{%s unchanged}]", skipped, same.ID.Hex())
	}
}

func TestPlanBulkAssign_NoOp(t *testing.T) {
	venue := bson.NewObjectID()
	target := bson.NewObjectID()
	p := Principal{VenueIDs: map[string]struct{}{venue.Hex(): {}}}
	already := &models.Asset{ID: bson.NewObjectID(), HomeVenueID: venue, CurrentVenueID: venue, ResponsibleUserID: &target}
	needs := &models.Asset{ID: bson.NewObjectID(), HomeVenueID: venue, CurrentVenueID: venue}
	byID := map[bson.ObjectID]*models.Asset{already.ID: already, needs.ID: needs}
	lookup := func(id bson.ObjectID) (*models.Asset, error) { return byID[id], nil }
	in := models.BulkAssignRequest{AssetIDs: []bson.ObjectID{already.ID, needs.ID}, ResponsibleUserID: target}
	toUpdate, skipped, err := planBulkAssign(in, p, lookup, 5000)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(toUpdate) != 1 || toUpdate[0] != needs.ID {
		t.Fatalf("toUpdate = %v, want [%s]", toUpdate, needs.ID.Hex())
	}
	if len(skipped) != 1 || skipped[0].Reason != "unchanged" {
		t.Fatalf("skipped = %+v, want one unchanged", skipped)
	}
}
