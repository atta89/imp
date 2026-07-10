package service

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
)

// resolvedRow is a tiny constructor for table-driven tests. Sets PONumber +
// CategoryID + LineItemName + Quantity at minimum — that's what the rollup
// and grouping helpers key on.
func mkResolved(po string, catID bson.ObjectID, name string, qty int, override AssetOverride) ResolvedRow {
	return ResolvedRow{
		Row:      models.ImportRow{PONumber: po, RowNum: 2},
		PO:       ResolvedPOHeader{PONumber: po},
		LineItem: ResolvedLineItem{CategoryID: catID, Name: name},
		Quantity: qty,
		Override: override,
	}
}

func TestGroupByPONumber_PreservesInputOrder(t *testing.T) {
	cat := bson.NewObjectID()
	rows := []ResolvedRow{
		mkResolved("PO-B", cat, "A", 1, AssetOverride{}),
		mkResolved("PO-A", cat, "A", 1, AssetOverride{}),
		mkResolved("PO-B", cat, "A", 1, AssetOverride{}),
	}
	groups := groupByPONumber(rows)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if groups[0].poNumber != "PO-B" || groups[1].poNumber != "PO-A" {
		t.Errorf("expected order PO-B, PO-A; got %s, %s", groups[0].poNumber, groups[1].poNumber)
	}
	if len(groups[0].rows) != 2 {
		t.Errorf("expected PO-B to have 2 rows, got %d", len(groups[0].rows))
	}
}

func TestRollupLineItems_SumsQuantitiesPerKey(t *testing.T) {
	cat := bson.NewObjectID()
	rows := []ResolvedRow{
		mkResolved("PO-1", cat, "MacBook", 2, AssetOverride{}),
		mkResolved("PO-1", cat, "MacBook", 3, AssetOverride{}),
		mkResolved("PO-1", cat, "Mouse", 10, AssetOverride{}),
	}
	items, _ := rollupLineItems(rows)
	if len(items) != 2 {
		t.Fatalf("expected 2 line items, got %d", len(items))
	}
	if items[0].Name != "MacBook" || items[0].Quantity != 5 {
		t.Errorf("expected MacBook qty 5, got %s qty %d", items[0].Name, items[0].Quantity)
	}
	if items[1].Name != "Mouse" || items[1].Quantity != 10 {
		t.Errorf("expected Mouse qty 10, got %s qty %d", items[1].Name, items[1].Quantity)
	}
}

func TestRollupLineItems_CaseInsensitiveNameMatching(t *testing.T) {
	cat := bson.NewObjectID()
	rows := []ResolvedRow{
		mkResolved("PO-1", cat, "MacBook", 2, AssetOverride{}),
		mkResolved("PO-1", cat, "macbook", 3, AssetOverride{}),
	}
	items, _ := rollupLineItems(rows)
	if len(items) != 1 || items[0].Quantity != 5 {
		t.Errorf("expected single rolled-up item, got %+v", items)
	}
}

func TestBuildOverrides_EmptyWhenAllRowsBlank(t *testing.T) {
	cat := bson.NewObjectID()
	rows := []ResolvedRow{
		mkResolved("PO-1", cat, "MacBook", 5, AssetOverride{}),
	}
	items, _ := rollupLineItems(rows)
	ov := buildOverrides(items, rows)
	if len(ov) != 1 || len(ov[0].PerUnit) != 0 {
		t.Errorf("expected empty PerUnit when no overrides set, got %+v", ov)
	}
}

func TestBuildOverrides_PerUnitWhenAnyOverridePresent(t *testing.T) {
	cat := bson.NewObjectID()
	tag1, tag2 := "LAP-0001", "LAP-0002"
	rows := []ResolvedRow{
		mkResolved("PO-1", cat, "MacBook", 1, AssetOverride{AssetTag: &tag1}),
		mkResolved("PO-1", cat, "MacBook", 1, AssetOverride{AssetTag: &tag2}),
	}
	items, _ := rollupLineItems(rows)
	ov := buildOverrides(items, rows)
	if len(ov) != 1 {
		t.Fatalf("expected 1 line, got %d", len(ov))
	}
	if len(ov[0].PerUnit) != 2 {
		t.Fatalf("expected 2 unit overrides, got %d", len(ov[0].PerUnit))
	}
	if ov[0].PerUnit[0].AssetTag == nil || *ov[0].PerUnit[0].AssetTag != "LAP-0001" {
		t.Errorf("expected unit 0 = LAP-0001, got %v", ov[0].PerUnit[0].AssetTag)
	}
	if ov[0].PerUnit[1].AssetTag == nil || *ov[0].PerUnit[1].AssetTag != "LAP-0002" {
		t.Errorf("expected unit 1 = LAP-0002, got %v", ov[0].PerUnit[1].AssetTag)
	}
}

func TestComputePreviewCounts_BasicCases(t *testing.T) {
	cat := bson.NewObjectID()
	resolved := []ResolvedRow{
		mkResolved("PO-1", cat, "A", 5, AssetOverride{}),
		mkResolved("PO-1", cat, "B", 3, AssetOverride{}),
		mkResolved("PO-2", cat, "A", 2, AssetOverride{}),
	}
	skipped := mkResolved("PO-3", cat, "A", 10, AssetOverride{})
	skipped.SkipExisting = true
	resolved = append(resolved, skipped)
	errs := []models.ImportRowError{makeRowError(99, "x", "fail")}

	c := computePreviewCounts(resolved, errs)
	if c.PosTotal != 2 {
		t.Errorf("PosTotal: want 2 (PO-3 skipped), got %d", c.PosTotal)
	}
	if c.AssetsCreated != 10 {
		t.Errorf("AssetsCreated preview: want 10, got %d", c.AssetsCreated)
	}
	if c.RowsSkipped != 10 {
		t.Errorf("RowsSkipped: want 10, got %d", c.RowsSkipped)
	}
	if c.RowsErrored != 1 {
		t.Errorf("RowsErrored: want 1, got %d", c.RowsErrored)
	}
}

func TestBuildPosPreview_GroupsPerPO(t *testing.T) {
	catA := bson.NewObjectID()
	catB := bson.NewObjectID()
	venue := bson.NewObjectID()
	rows := []ResolvedRow{
		{
			PO:       ResolvedPOHeader{PONumber: "PO-1"},
			LineItem: ResolvedLineItem{CategoryID: catA, CategorySlug: "laptop", Name: "MacBook", HomeVenueID: venue},
			Quantity: 5,
		},
		{
			PO:       ResolvedPOHeader{PONumber: "PO-1"},
			LineItem: ResolvedLineItem{CategoryID: catB, CategorySlug: "chair", Name: "Office Chair", HomeVenueID: venue},
			Quantity: 3,
		},
	}
	preview := buildPosPreview(rows)
	if len(preview) != 1 {
		t.Fatalf("expected 1 PO entry, got %d", len(preview))
	}
	p := preview[0]
	if p.PoNumber != "PO-1" {
		t.Errorf("PoNumber: %s", p.PoNumber)
	}
	if p.LineItems != 2 {
		t.Errorf("LineItems: want 2, got %d", p.LineItems)
	}
	if p.AssetCount != 8 {
		t.Errorf("AssetCount: want 8, got %d", p.AssetCount)
	}
	if p.Categories == nil || len(*p.Categories) != 2 {
		t.Errorf("Categories: want 2, got %v", p.Categories)
	}
}

func TestIsZeroOverride(t *testing.T) {
	if !isZeroOverride(AssetOverride{}) {
		t.Error("zero AssetOverride should be detected as zero")
	}
	tag := "X"
	if isZeroOverride(AssetOverride{AssetTag: &tag}) {
		t.Error("AssetOverride with tag should not be zero")
	}
	now := time.Now()
	if isZeroOverride(AssetOverride{PurchaseDate: &now}) {
		t.Error("AssetOverride with date should not be zero")
	}
}

func TestDerefConflict_DefaultsToError(t *testing.T) {
	if derefConflict(nil) != models.Error {
		t.Errorf("nil should default to Error, got %s", derefConflict(nil))
	}
	skip := models.SkipExisting
	if derefConflict(&skip) != models.SkipExisting {
		t.Errorf("ptr-skipExisting should return SkipExisting")
	}
}

func TestPoGroup_SkipExistingDetection(t *testing.T) {
	cat := bson.NewObjectID()
	r1 := mkResolved("PO-1", cat, "A", 1, AssetOverride{})
	r2 := mkResolved("PO-1", cat, "A", 1, AssetOverride{})
	r2.SkipExisting = true
	g := poGroup{poNumber: "PO-1", rows: []ResolvedRow{r1, r2}}
	if !g.skipExisting() {
		t.Error("group should be SkipExisting when any row is flagged")
	}
}

func TestCountDistinctPOs_IgnoresSkipped(t *testing.T) {
	cat := bson.NewObjectID()
	r1 := mkResolved("PO-1", cat, "A", 1, AssetOverride{})
	r2 := mkResolved("PO-2", cat, "A", 1, AssetOverride{})
	r3 := mkResolved("PO-3", cat, "A", 1, AssetOverride{})
	r3.SkipExisting = true
	got := countDistinctPOs([]ResolvedRow{r1, r2, r3})
	if got != 2 {
		t.Errorf("want 2 (skipped excluded), got %d", got)
	}
}
