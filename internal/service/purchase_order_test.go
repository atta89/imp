package service

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
)

func TestResolveAssetFields_AppliesDefaultsWhenNoOverrides(t *testing.T) {
	venueID := bson.NewObjectID()
	poOwner := bson.NewObjectID()
	po := &models.PurchaseOrder{ResponsibleUserID: poOwner}
	receivedAt := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)

	got := resolveAssetFields(ReceiveOptions{VenueID: venueID}, po, receivedAt, AssetOverride{})

	if got.Status != models.Available {
		t.Errorf("Status: want available, got %s", got.Status)
	}
	if got.Condition != models.New {
		t.Errorf("Condition: want new, got %s", got.Condition)
	}
	if got.HomeVenueID != venueID {
		t.Errorf("HomeVenueID: want %s, got %s", venueID.Hex(), got.HomeVenueID.Hex())
	}
	if got.CurrentVenueID != venueID {
		t.Errorf("CurrentVenueID: want HomeVenueID (%s), got %s", venueID.Hex(), got.CurrentVenueID.Hex())
	}
	if got.ResponsibleUserID == nil || *got.ResponsibleUserID != poOwner {
		t.Errorf("ResponsibleUserID: want PO owner (%s), got %v", poOwner.Hex(), got.ResponsibleUserID)
	}
	if got.PurchaseDate == nil || !got.PurchaseDate.Equal(receivedAt) {
		t.Errorf("PurchaseDate: want receivedAt, got %v", got.PurchaseDate)
	}
	if got.AssetTag != "" {
		t.Errorf("AssetTag: want empty (auto-generate), got %q", got.AssetTag)
	}
}

func TestResolveAssetFields_EachFieldOverridable(t *testing.T) {
	venueID := bson.NewObjectID()
	overrideVenue := bson.NewObjectID()
	currentVenue := bson.NewObjectID()
	poOwner := bson.NewObjectID()
	overrideOwner := bson.NewObjectID()
	po := &models.PurchaseOrder{ResponsibleUserID: poOwner}
	receivedAt := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	customDate := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	tag := "  LAP-9999  "
	status := models.InUse
	cond := models.Fair
	serial := "SN-ABC123"
	notes := "imported from 2024 sheet"

	ov := AssetOverride{
		AssetTag:          &tag,
		Status:            &status,
		Condition:         &cond,
		HomeVenueID:       &overrideVenue,
		CurrentVenueID:    &currentVenue,
		ResponsibleUserID: &overrideOwner,
		SerialNumber:      &serial,
		PurchaseDate:      &customDate,
		Notes:             &notes,
	}

	got := resolveAssetFields(ReceiveOptions{VenueID: venueID}, po, receivedAt, ov)

	if got.AssetTag != "LAP-9999" {
		t.Errorf("AssetTag: want trimmed LAP-9999, got %q", got.AssetTag)
	}
	if got.Status != models.InUse {
		t.Errorf("Status: want in_use, got %s", got.Status)
	}
	if got.Condition != models.Fair {
		t.Errorf("Condition: want fair, got %s", got.Condition)
	}
	if got.HomeVenueID != overrideVenue {
		t.Errorf("HomeVenueID: want override, got %s", got.HomeVenueID.Hex())
	}
	if got.CurrentVenueID != currentVenue {
		t.Errorf("CurrentVenueID: want override current, got %s", got.CurrentVenueID.Hex())
	}
	if got.ResponsibleUserID == nil || *got.ResponsibleUserID != overrideOwner {
		t.Errorf("ResponsibleUserID: want override owner, got %v", got.ResponsibleUserID)
	}
	if got.SerialNumber == nil || *got.SerialNumber != serial {
		t.Errorf("SerialNumber: want %s, got %v", serial, got.SerialNumber)
	}
	if got.PurchaseDate == nil || !got.PurchaseDate.Equal(customDate) {
		t.Errorf("PurchaseDate: want customDate, got %v", got.PurchaseDate)
	}
	if got.Notes == nil || *got.Notes != notes {
		t.Errorf("Notes: want %q, got %v", notes, got.Notes)
	}
}

func TestResolveAssetFields_HomeVenueOverrideCascadesToCurrentVenue(t *testing.T) {
	defaultVenue := bson.NewObjectID()
	overrideHome := bson.NewObjectID()
	po := &models.PurchaseOrder{ResponsibleUserID: bson.NewObjectID()}

	got := resolveAssetFields(
		ReceiveOptions{VenueID: defaultVenue},
		po,
		time.Now(),
		AssetOverride{HomeVenueID: &overrideHome},
	)

	if got.HomeVenueID != overrideHome {
		t.Errorf("HomeVenueID: want override, got %s", got.HomeVenueID.Hex())
	}
	if got.CurrentVenueID != overrideHome {
		t.Errorf("CurrentVenueID: want to follow override home, got %s", got.CurrentVenueID.Hex())
	}
}

func TestResolveAssetFields_DepartmentOverrideBeatsOptsDefault(t *testing.T) {
	venue := bson.NewObjectID()
	optsDept := bson.NewObjectID()
	overrideDept := bson.NewObjectID()
	po := &models.PurchaseOrder{ResponsibleUserID: bson.NewObjectID()}

	got := resolveAssetFields(
		ReceiveOptions{VenueID: venue, DepartmentID: &optsDept},
		po,
		time.Now(),
		AssetOverride{DepartmentID: &overrideDept},
	)
	if got.DepartmentID == nil || *got.DepartmentID != overrideDept {
		t.Errorf("DepartmentID: want override, got %v", got.DepartmentID)
	}
}

func TestResolveAssetFields_DepartmentOptsUsedWhenNoOverride(t *testing.T) {
	venue := bson.NewObjectID()
	optsDept := bson.NewObjectID()
	po := &models.PurchaseOrder{ResponsibleUserID: bson.NewObjectID()}

	got := resolveAssetFields(
		ReceiveOptions{VenueID: venue, DepartmentID: &optsDept},
		po,
		time.Now(),
		AssetOverride{},
	)
	if got.DepartmentID == nil || *got.DepartmentID != optsDept {
		t.Errorf("DepartmentID: want opts default, got %v", got.DepartmentID)
	}
}

func TestLineItemOverridesAt_OutOfRangeReturnsZero(t *testing.T) {
	o := []LineItemOverrides{{PerUnit: []AssetOverride{{}}}}
	if got := lineItemOverridesAt(o, 0); len(got.PerUnit) != 1 {
		t.Errorf("expected index 0 to return the populated entry")
	}
	if got := lineItemOverridesAt(o, 1); len(got.PerUnit) != 0 {
		t.Errorf("expected past-end index to return zero value, got %+v", got)
	}
	if got := lineItemOverridesAt(nil, 0); len(got.PerUnit) != 0 {
		t.Errorf("nil slice should return zero value, got %+v", got)
	}
}

func TestAssetOverrideAt_OutOfRangeReturnsZero(t *testing.T) {
	tag := "LAP-0001"
	line := LineItemOverrides{PerUnit: []AssetOverride{{AssetTag: &tag}}}
	if got := assetOverrideAt(line, 0); got.AssetTag == nil || *got.AssetTag != tag {
		t.Errorf("index 0 should return populated override")
	}
	if got := assetOverrideAt(line, 1); got.AssetTag != nil {
		t.Errorf("past-end should return zero AssetOverride, got %+v", got)
	}
	if got := assetOverrideAt(LineItemOverrides{}, 0); got.AssetTag != nil {
		t.Errorf("empty PerUnit should return zero, got %+v", got)
	}
}

func TestTagPool_TakeIsSequentialPerPrefix(t *testing.T) {
	p := newTagPool(map[string]int64{"LAP": 5, "MON": 1})
	got := []string{
		p.take("LAP"), p.take("LAP"), p.take("MON"), p.take("LAP"),
	}
	want := []string{"LAP-0005", "LAP-0006", "MON-0001", "LAP-0007"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("take[%d]: want %s, got %s", i, want[i], got[i])
		}
	}
}

// buildReceiveDocs is the pure planning step behind ReceiveWithOptions: given a
// PO, options, categories and a pre-reserved tag pool, it produces the exact
// asset + movement documents to bulk-insert. No DB access — the batching
// correctness (counts, tag assignment, movement pairing) lives here.
func newTestBuildInputs() (*models.PurchaseOrder, map[bson.ObjectID]*models.Category) {
	catA := bson.NewObjectID()
	catB := bson.NewObjectID()
	po := &models.PurchaseOrder{
		ID:                bson.NewObjectID(),
		ResponsibleUserID: bson.NewObjectID(),
		LineItems: []models.PurchaseOrderLineItem{
			{Name: "Laptop", CategoryID: catA, Quantity: 2},
			{Name: "Monitor", CategoryID: catB, Quantity: 3},
		},
	}
	cats := map[bson.ObjectID]*models.Category{
		catA: {ID: catA, Slug: "laptop"},
		catB: {ID: catB, Slug: "monitor"},
	}
	return po, cats
}

func TestBuildReceiveDocs_CountsTagsAndIDs(t *testing.T) {
	po, cats := newTestBuildInputs()
	venue := bson.NewObjectID()
	pool := newTagPool(map[string]int64{"LAP": 1, "MON": 1})

	assets, movements, err := buildReceiveDocs(po, ReceiveOptions{VenueID: venue}, cats, time.Now().UTC(), pool)
	if err != nil {
		t.Fatalf("buildReceiveDocs: %v", err)
	}
	if len(assets) != 5 {
		t.Fatalf("asset count: want 5, got %d", len(assets))
	}
	if len(movements) != 0 {
		t.Errorf("movements: want 0 without ImportJobID, got %d", len(movements))
	}
	wantTags := []string{"LAP-0001", "LAP-0002", "MON-0001", "MON-0002", "MON-0003"}
	seenIDs := map[bson.ObjectID]bool{}
	for i, a := range assets {
		if a.AssetTag != wantTags[i] {
			t.Errorf("asset[%d] tag: want %s, got %s", i, wantTags[i], a.AssetTag)
		}
		if a.ID.IsZero() {
			t.Errorf("asset[%d] ID not assigned", i)
		}
		if seenIDs[a.ID] {
			t.Errorf("asset[%d] duplicate ID %s", i, a.ID.Hex())
		}
		seenIDs[a.ID] = true
		if a.QrToken == "" {
			t.Errorf("asset[%d] QrToken empty", i)
		}
		if a.PurchaseOrderID == nil || *a.PurchaseOrderID != po.ID {
			t.Errorf("asset[%d] PurchaseOrderID: want %s, got %v", i, po.ID.Hex(), a.PurchaseOrderID)
		}
	}
}

func TestBuildReceiveDocs_OverrideTagSkipsPool(t *testing.T) {
	po, cats := newTestBuildInputs()
	tag := "LAP-9999"
	opts := ReceiveOptions{
		VenueID: bson.NewObjectID(),
		Overrides: []LineItemOverrides{
			{PerUnit: []AssetOverride{{AssetTag: &tag}, {}}}, // line 0, unit 0 overrides tag
		},
	}
	pool := newTagPool(map[string]int64{"LAP": 1, "MON": 1})

	assets, _, err := buildReceiveDocs(po, opts, cats, time.Now().UTC(), pool)
	if err != nil {
		t.Fatalf("buildReceiveDocs: %v", err)
	}
	if assets[0].AssetTag != "LAP-9999" {
		t.Errorf("asset[0]: want override LAP-9999, got %s", assets[0].AssetTag)
	}
	// Unit 1 of the laptop line should still draw the FIRST reserved LAP seq,
	// because the override consumed none of the pool.
	if assets[1].AssetTag != "LAP-0001" {
		t.Errorf("asset[1]: want LAP-0001 (pool untouched by override), got %s", assets[1].AssetTag)
	}
}

func TestBuildReceiveDocs_ImportCreatesPairedMovements(t *testing.T) {
	po, cats := newTestBuildInputs()
	jobID := bson.NewObjectID()
	performer := bson.NewObjectID()
	opts := ReceiveOptions{
		VenueID:     bson.NewObjectID(),
		ImportJobID: &jobID,
		PerformedBy: &performer,
	}
	pool := newTagPool(map[string]int64{"LAP": 1, "MON": 1})

	assets, movements, err := buildReceiveDocs(po, opts, cats, time.Now().UTC(), pool)
	if err != nil {
		t.Fatalf("buildReceiveDocs: %v", err)
	}
	// Every asset has a responsible user (PO owner default), so one movement each.
	if len(movements) != len(assets) {
		t.Fatalf("movements: want %d, got %d", len(assets), len(movements))
	}
	assetIDs := map[bson.ObjectID]bool{}
	for _, a := range assets {
		assetIDs[a.ID] = true
	}
	for i, m := range movements {
		if !assetIDs[m.AssetID] {
			t.Errorf("movement[%d] AssetID %s not among built assets", i, m.AssetID.Hex())
		}
		if m.Type != models.MovementTypeCustodyChange {
			t.Errorf("movement[%d] type: want custody_change, got %s", i, m.Type)
		}
		if m.PerformedBy != performer {
			t.Errorf("movement[%d] PerformedBy: want performer, got %s", i, m.PerformedBy.Hex())
		}
		if m.Reason == nil || *m.Reason != "import" {
			t.Errorf("movement[%d] reason: want import, got %v", i, m.Reason)
		}
	}
}

func TestResolveAssetFields_DepartmentDefaultAndNilStay(t *testing.T) {
	// No opts default, no override → resolved DepartmentID stays nil.
	po := &models.PurchaseOrder{ResponsibleUserID: bson.NewObjectID()}
	got := resolveAssetFields(ReceiveOptions{VenueID: bson.NewObjectID()}, po, time.Now(), AssetOverride{})
	if got.DepartmentID != nil {
		t.Errorf("DepartmentID: want nil when neither set, got %v", got.DepartmentID)
	}
}
