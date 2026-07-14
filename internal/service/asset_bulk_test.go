package service

import (
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
)

func mkAsset(id, home, current bson.ObjectID, status models.AssetStatus) *models.Asset {
	return &models.Asset{
		ID:             id,
		HomeVenueID:    home,
		CurrentVenueID: current,
		Status:         status,
	}
}

func okPrincipal(venues ...bson.ObjectID) Principal {
	set := make(map[string]struct{}, len(venues))
	for _, v := range venues {
		set[v.Hex()] = struct{}{}
	}
	return Principal{VenueIDs: set}
}

func TestValidateBulkTransfer_HappyPath(t *testing.T) {
	a, b := bson.NewObjectID(), bson.NewObjectID()
	home, dest := bson.NewObjectID(), bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{
		a: mkAsset(a, home, home, models.Available),
		b: mkAsset(b, home, home, models.InUse),
	}
	in := models.BulkTransferRequest{AssetIDs: []bson.ObjectID{a, b}, ToVenueID: dest}
	oks, res, allOK := validateBulkTransferRequest(in, okPrincipal(home, dest),
		func(id bson.ObjectID) (*models.Asset, error) { return store[id], nil }, true, MaxBulkAssets)
	if !allOK {
		t.Fatalf("expected allOK; results=%+v", res)
	}
	if len(oks) != 2 || len(res) != 2 {
		t.Fatalf("expected 2/2; got oks=%d res=%d", len(oks), len(res))
	}
	for _, r := range res {
		if !r.Ok {
			t.Errorf("row %s not ok: %v", r.AssetID.Hex(), r.Error)
		}
	}
}

func TestValidateBulkTransfer_CapsAtMaxBulkAssets(t *testing.T) {
	dest := bson.NewObjectID()
	ids := make([]bson.ObjectID, MaxBulkAssets+1)
	for i := range ids {
		ids[i] = bson.NewObjectID()
	}
	in := models.BulkTransferRequest{AssetIDs: ids, ToVenueID: dest}
	_, _, allOK := validateBulkTransferRequest(in, Principal{IsAdmin: true},
		func(id bson.ObjectID) (*models.Asset, error) { return nil, apperror.NotFound("x") }, true, MaxBulkAssets)
	if allOK {
		t.Error("expected allOK=false when over cap")
	}
}

func TestValidateBulkTransfer_RbacRejectsOutOfScopeAsset(t *testing.T) {
	a := bson.NewObjectID()
	home, dest := bson.NewObjectID(), bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{a: mkAsset(a, home, home, models.Available)}
	// principal has access to dest but not to home — should be rejected.
	in := models.BulkTransferRequest{AssetIDs: []bson.ObjectID{a}, ToVenueID: dest}
	_, res, allOK := validateBulkTransferRequest(in, okPrincipal(dest),
		func(id bson.ObjectID) (*models.Asset, error) { return store[id], nil }, true, MaxBulkAssets)
	if allOK {
		t.Error("expected failure")
	}
	if res[0].Ok || res[0].Error == nil || *res[0].Error != "forbidden" {
		t.Errorf("expected forbidden; got %+v", res[0])
	}
}

func TestValidateBulkTransfer_RejectsDestVenueOutOfScope(t *testing.T) {
	a := bson.NewObjectID()
	home, dest := bson.NewObjectID(), bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{a: mkAsset(a, home, home, models.Available)}
	in := models.BulkTransferRequest{AssetIDs: []bson.ObjectID{a}, ToVenueID: dest}
	_, res, allOK := validateBulkTransferRequest(in, okPrincipal(home),
		func(id bson.ObjectID) (*models.Asset, error) { return store[id], nil }, true, MaxBulkAssets)
	if allOK || res[0].Ok || res[0].Error == nil || *res[0].Error != "dest_venue_forbidden" {
		t.Errorf("expected dest_venue_forbidden; got %+v", res[0])
	}
}

func TestValidateBulkTransfer_RejectsSameVenueMove(t *testing.T) {
	a := bson.NewObjectID()
	home := bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{a: mkAsset(a, home, home, models.Available)}
	in := models.BulkTransferRequest{AssetIDs: []bson.ObjectID{a}, ToVenueID: home}
	_, res, allOK := validateBulkTransferRequest(in, Principal{IsAdmin: true},
		func(id bson.ObjectID) (*models.Asset, error) { return store[id], nil }, true, MaxBulkAssets)
	if allOK || res[0].Ok {
		t.Errorf("expected rejection; got %+v", res[0])
	}
}

func TestValidateBulkTransfer_NotFoundIsPerRow(t *testing.T) {
	a := bson.NewObjectID()
	dest := bson.NewObjectID()
	in := models.BulkTransferRequest{AssetIDs: []bson.ObjectID{a}, ToVenueID: dest}
	_, res, allOK := validateBulkTransferRequest(in, Principal{IsAdmin: true},
		func(id bson.ObjectID) (*models.Asset, error) { return nil, apperror.NotFound("missing") }, true, MaxBulkAssets)
	if allOK || res[0].Ok || res[0].Error == nil || *res[0].Error != "not_found" {
		t.Errorf("expected not_found; got %+v", res[0])
	}
}

func TestValidateBulkTransfer_RejectsDuplicateAssetIDs(t *testing.T) {
	a := bson.NewObjectID()
	home, dest := bson.NewObjectID(), bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{a: mkAsset(a, home, home, models.Available)}
	in := models.BulkTransferRequest{AssetIDs: []bson.ObjectID{a, a}, ToVenueID: dest}
	_, _, allOK := validateBulkTransferRequest(in, Principal{IsAdmin: true},
		func(id bson.ObjectID) (*models.Asset, error) { return store[id], nil }, true, MaxBulkAssets)
	if allOK {
		t.Error("expected duplicate-ID rejection")
	}
}

func TestValidateBulkStatus_RejectsIllegalTransitions(t *testing.T) {
	a, b := bson.NewObjectID(), bson.NewObjectID()
	home := bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{
		a: mkAsset(a, home, home, models.Retired), // retired -> in_use is illegal
		b: mkAsset(b, home, home, models.Available),
	}
	in := models.BulkStatusRequest{AssetIDs: []bson.ObjectID{a, b}, Status: models.InUse}
	_, res, allOK := validateBulkStatusRequest(in, Principal{IsAdmin: true},
		func(id bson.ObjectID) (*models.Asset, error) { return store[id], nil }, MaxBulkAssets)
	if allOK {
		t.Error("expected allOK=false because of illegal transition on a")
	}
	if res[0].Ok || res[0].Error == nil || *res[0].Error != "invalid_transition" {
		t.Errorf("row 0: expected invalid_transition; got %+v", res[0])
	}
	if !res[1].Ok {
		t.Errorf("row 1: expected ok; got %+v", res[1])
	}
}

// mkAssetC is mkAsset extended with a condition — the bulk-condition planner
// keys off Condition + Home/CurrentVenueID, not Status.
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
func skippedByID(sk []models.BulkConditionSkipped) map[bson.ObjectID]string {
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

// mkAssetU is mkAsset with a responsibleUserId; bulk-assign classification
// keys off ResponsibleUserID so tests need it explicit.
func mkAssetU(id, home bson.ObjectID, responsibleUserID *bson.ObjectID) *models.Asset {
	return &models.Asset{
		ID:                id,
		HomeVenueID:       home,
		CurrentVenueID:    home,
		ResponsibleUserID: responsibleUserID,
	}
}

func TestValidateBulkAssign_HappyPath(t *testing.T) {
	a, b := bson.NewObjectID(), bson.NewObjectID()
	home := bson.NewObjectID()
	oldU := bson.NewObjectID()
	newU := bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{
		a: mkAssetU(a, home, &oldU),
		b: mkAssetU(b, home, nil), // no prior custodian
	}
	in := models.BulkAssignRequest{AssetIDs: []bson.ObjectID{a, b}, ResponsibleUserID: newU}
	oks, res, allOK := validateBulkAssignRequest(in, okPrincipal(home),
		func(id bson.ObjectID) (*models.Asset, error) { return store[id], nil }, MaxBulkAssets)
	if !allOK {
		t.Fatalf("expected allOK; results=%+v", res)
	}
	if len(oks) != 2 || len(res) != 2 {
		t.Fatalf("expected 2/2; got oks=%d res=%d", len(oks), len(res))
	}
	for _, v := range oks {
		if v.noOp {
			t.Errorf("asset %s classified as no-op but should update", v.asset.ID.Hex())
		}
	}
	for _, r := range res {
		if !r.Ok {
			t.Errorf("row %s not ok: %v", r.AssetID.Hex(), r.Error)
		}
	}
}

func TestValidateBulkAssign_ClassifiesNoOps(t *testing.T) {
	a, b := bson.NewObjectID(), bson.NewObjectID()
	home := bson.NewObjectID()
	newU := bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{
		a: mkAssetU(a, home, &newU), // already assigned — no-op
		b: mkAssetU(b, home, nil),   // needs assign
	}
	in := models.BulkAssignRequest{AssetIDs: []bson.ObjectID{a, b}, ResponsibleUserID: newU}
	oks, res, allOK := validateBulkAssignRequest(in, okPrincipal(home),
		func(id bson.ObjectID) (*models.Asset, error) { return store[id], nil }, MaxBulkAssets)
	if !allOK {
		t.Fatalf("expected allOK; results=%+v", res)
	}
	if len(oks) != 2 {
		t.Fatalf("expected 2 oks; got %d", len(oks))
	}
	byID := map[bson.ObjectID]bool{}
	for _, v := range oks {
		byID[v.asset.ID] = v.noOp
	}
	if !byID[a] {
		t.Errorf("asset a should be a no-op (already assigned)")
	}
	if byID[b] {
		t.Errorf("asset b should NOT be a no-op (nil prior custodian)")
	}
	for _, r := range res {
		if !r.Ok {
			t.Errorf("row %s not ok: %v", r.AssetID.Hex(), r.Error)
		}
	}
}

func TestValidateBulkAssign_EmptyBatch(t *testing.T) {
	in := models.BulkAssignRequest{AssetIDs: nil, ResponsibleUserID: bson.NewObjectID()}
	_, _, allOK := validateBulkAssignRequest(in, Principal{IsAdmin: true},
		func(id bson.ObjectID) (*models.Asset, error) { return nil, nil }, MaxBulkAssets)
	if allOK {
		t.Error("expected empty batch to fail")
	}
}

func TestValidateBulkAssign_CapsAtMaxBulkAssets(t *testing.T) {
	ids := make([]bson.ObjectID, MaxBulkAssets+1)
	for i := range ids {
		ids[i] = bson.NewObjectID()
	}
	in := models.BulkAssignRequest{AssetIDs: ids, ResponsibleUserID: bson.NewObjectID()}
	_, _, allOK := validateBulkAssignRequest(in, Principal{IsAdmin: true},
		func(id bson.ObjectID) (*models.Asset, error) { return nil, apperror.NotFound("x") }, MaxBulkAssets)
	if allOK {
		t.Error("expected over-cap batch to fail")
	}
}

func TestValidateBulkAssign_RbacRejectsOutOfScopeAsset(t *testing.T) {
	a := bson.NewObjectID()
	home := bson.NewObjectID()
	otherVenue := bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{a: mkAssetU(a, home, nil)}
	in := models.BulkAssignRequest{AssetIDs: []bson.ObjectID{a}, ResponsibleUserID: bson.NewObjectID()}
	// principal only has access to a venue the asset is not tied to.
	_, res, allOK := validateBulkAssignRequest(in, okPrincipal(otherVenue),
		func(id bson.ObjectID) (*models.Asset, error) { return store[id], nil }, MaxBulkAssets)
	if allOK {
		t.Error("expected forbidden failure")
	}
	if res[0].Ok || res[0].Error == nil || *res[0].Error != "forbidden" {
		t.Errorf("expected forbidden; got %+v", res[0])
	}
}

func TestValidateBulkAssign_NotFoundIsPerRow(t *testing.T) {
	a := bson.NewObjectID()
	in := models.BulkAssignRequest{AssetIDs: []bson.ObjectID{a}, ResponsibleUserID: bson.NewObjectID()}
	_, res, allOK := validateBulkAssignRequest(in, Principal{IsAdmin: true},
		func(id bson.ObjectID) (*models.Asset, error) { return nil, apperror.NotFound("missing") }, MaxBulkAssets)
	if allOK || res[0].Ok || res[0].Error == nil || *res[0].Error != "not_found" {
		t.Errorf("expected not_found; got %+v", res[0])
	}
}

func TestValidateBulkAssign_RejectsDuplicateAssetIDs(t *testing.T) {
	a := bson.NewObjectID()
	home := bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{a: mkAssetU(a, home, nil)}
	in := models.BulkAssignRequest{AssetIDs: []bson.ObjectID{a, a}, ResponsibleUserID: bson.NewObjectID()}
	_, res, allOK := validateBulkAssignRequest(in, Principal{IsAdmin: true},
		func(id bson.ObjectID) (*models.Asset, error) { return store[id], nil }, MaxBulkAssets)
	if allOK {
		t.Error("expected duplicate-id rejection")
	}
	// Second row is the duplicate.
	if res[1].Ok || res[1].Error == nil || *res[1].Error != "duplicate_id" {
		t.Errorf("expected duplicate_id on row 1; got %+v", res[1])
	}
}

func TestValidateBulkAssign_AdminBypassesVenueScope(t *testing.T) {
	v1, v2 := bson.NewObjectID(), bson.NewObjectID()
	a, b := bson.NewObjectID(), bson.NewObjectID()
	store := map[bson.ObjectID]*models.Asset{
		a: mkAssetU(a, v1, nil),
		b: mkAssetU(b, v2, nil),
	}
	in := models.BulkAssignRequest{AssetIDs: []bson.ObjectID{a, b}, ResponsibleUserID: bson.NewObjectID()}
	oks, res, allOK := validateBulkAssignRequest(in, Principal{IsAdmin: true},
		func(id bson.ObjectID) (*models.Asset, error) { return store[id], nil }, MaxBulkAssets)
	if !allOK {
		t.Fatalf("admin should reach both assets; results=%+v", res)
	}
	if len(oks) != 2 {
		t.Errorf("expected 2 oks; got %d", len(oks))
	}
}

// buildBulkAssignResponse is the shape mapping tests: total/updated/skippedNoOp
// counts derive from the validated set (only ones with noOp=false are
// "updated"; noOp=true are "skippedNoOp").
func TestBulkAssignResponse_TalliesUpdatedAndSkipped(t *testing.T) {
	home := bson.NewObjectID()
	newU := bson.NewObjectID()
	a := mkAssetU(bson.NewObjectID(), home, nil)  // will update
	b := mkAssetU(bson.NewObjectID(), home, &newU) // no-op (same user)
	oks := []validatedAssign{{asset: a, noOp: false}, {asset: b, noOp: true}}
	results := []models.BulkActionResult{{AssetID: a.ID, Ok: true}, {AssetID: b.ID, Ok: true}}
	resp := bulkAssignResponse(oks, results)
	if resp.Total != 2 || resp.Updated != 1 || resp.SkippedNoOp != 1 {
		t.Errorf("counts wrong: total=%d updated=%d skipped=%d", resp.Total, resp.Updated, resp.SkippedNoOp)
	}
	if len(resp.Results) != 2 {
		t.Errorf("expected 2 results; got %d", len(resp.Results))
	}
}

// On failure path, response should report all-zero counts and per-row errors.
func TestBulkAssignResponse_FailedBatchReportsZeroCounts(t *testing.T) {
	results := []models.BulkActionResult{{AssetID: bson.NewObjectID(), Ok: false, Error: errString("not_found")}}
	resp := bulkAssignResponse(nil, results)
	if resp.Updated != 0 || resp.SkippedNoOp != 0 {
		t.Errorf("expected zero counts on failure; got updated=%d skipped=%d", resp.Updated, resp.SkippedNoOp)
	}
	if resp.Total != 1 || len(resp.Results) != 1 {
		t.Errorf("expected total=1 with 1 result row; got total=%d rows=%d", resp.Total, len(resp.Results))
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

func TestDedupeTransferNotifyTargets_OnePerHomeVenue(t *testing.T) {
	hv1, hv2 := bson.NewObjectID(), bson.NewObjectID()
	dest := bson.NewObjectID()
	a := mkAsset(bson.NewObjectID(), hv1, hv1, models.Available)
	b := mkAsset(bson.NewObjectID(), hv1, hv1, models.InUse)
	c := mkAsset(bson.NewObjectID(), hv2, hv2, models.Available)
	got := dedupeTransferNotifyTargets([]validatedTransfer{{asset: a}, {asset: b}, {asset: c}}, dest)
	if len(got) != 2 {
		t.Fatalf("expected 2 home-venue groups; got %d", len(got))
	}
	// hv1 group should carry 2 assets; hv2 group should carry 1.
	byVenue := map[bson.ObjectID]int{}
	for _, g := range got {
		byVenue[g.HomeVenueID] = len(g.Assets)
	}
	if byVenue[hv1] != 2 || byVenue[hv2] != 1 {
		t.Errorf("group sizes wrong: %v", byVenue)
	}
}
