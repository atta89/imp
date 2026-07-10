package service

import (
	"context"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
)

// --- in-memory fakes ----------------------------------------------------

type fakeUsers struct{ byEmail map[string]*models.User }

func (f *fakeUsers) FindByEmail(_ context.Context, email string) (*models.User, error) {
	u, ok := f.byEmail[strings.ToLower(email)]
	if !ok {
		return nil, apperror.NotFound("user not found")
	}
	return u, nil
}

type fakeCategories struct {
	bySlug map[string]*models.Category
	byName map[string]*models.Category
}

func (f *fakeCategories) FindBySlug(_ context.Context, slug string) (*models.Category, error) {
	c, ok := f.bySlug[strings.ToLower(slug)]
	if !ok {
		return nil, apperror.NotFound("category not found")
	}
	return c, nil
}
func (f *fakeCategories) FindByName(_ context.Context, name string) (*models.Category, error) {
	c, ok := f.byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, apperror.NotFound("category not found")
	}
	return c, nil
}

type fakeVenues struct{ byCode map[string]*models.Venue }

func (f *fakeVenues) FindByCode(_ context.Context, code string) (*models.Venue, error) {
	v, ok := f.byCode[code]
	if !ok {
		return nil, apperror.NotFound("venue not found")
	}
	return v, nil
}

type fakeAssets struct{ existing map[string]bool }

func (f *fakeAssets) ExistsByTag(_ context.Context, tag string) (bool, error) {
	return f.existing[tag], nil
}

type fakePOs struct{ existing map[string]bool }

func (f *fakePOs) ExistsByNumber(_ context.Context, n string) (bool, error) {
	return f.existing[n], nil
}

type fakeDepartments struct{ byVenueAndCode map[string]*models.Department }

func (f *fakeDepartments) FindByVenueAndCode(_ context.Context, venueID bson.ObjectID, code string) (*models.Department, error) {
	d, ok := f.byVenueAndCode[venueID.Hex()+"|"+code]
	if !ok {
		return nil, apperror.NotFound("department not found")
	}
	return d, nil
}

// fixture builds a Resolver with a small fixed environment used by most tests.
func fixture(t *testing.T) (*Resolver, fixtureIDs) {
	t.Helper()
	users := &fakeUsers{byEmail: map[string]*models.User{
		"pat@example.com": {ID: bson.NewObjectID(), Email: openapi_types.Email("pat@example.com"), Name: "Pat", IsActive: true},
		"sam@example.com": {ID: bson.NewObjectID(), Email: openapi_types.Email("sam@example.com"), Name: "Sam", IsActive: true},
		"old@example.com": {ID: bson.NewObjectID(), Email: openapi_types.Email("old@example.com"), Name: "Old", IsActive: false},
	}}
	laptop := &models.Category{ID: bson.NewObjectID(), Slug: "laptop", Name: "Laptop", CustomFields: []models.CategoryCustomField{
		{Key: "cpu", Label: "CPU", Type: models.String, Required: false},
		{Key: "ram", Label: "RAM (GB)", Type: models.Number, Required: false},
		{Key: "isPro", Label: "Pro?", Type: models.Boolean, Required: false},
		{Key: "warrantyUntil", Label: "Warranty until", Type: models.Date, Required: true},
	}}
	chair := &models.Category{ID: bson.NewObjectID(), Slug: "chair", Name: "Office Chair"}
	cats := &fakeCategories{
		bySlug: map[string]*models.Category{"laptop": laptop, "chair": chair},
		byName: map[string]*models.Category{"laptop": laptop, "office chair": chair},
	}
	hq := &models.Venue{ID: bson.NewObjectID(), Code: "HQ", Name: "HQ"}
	wh := &models.Venue{ID: bson.NewObjectID(), Code: "WH", Name: "Warehouse"}
	venues := &fakeVenues{byCode: map[string]*models.Venue{"HQ": hq, "WH": wh}}
	assets := &fakeAssets{existing: map[string]bool{"LAP-0001": true}}
	pos := &fakePOs{existing: map[string]bool{"PO-EXISTS": true}}
	departments := &fakeDepartments{byVenueAndCode: map[string]*models.Department{}}

	return NewResolver(users, cats, venues, assets, pos, departments), fixtureIDs{
		PatID:    users.byEmail["pat@example.com"].ID,
		SamID:    users.byEmail["sam@example.com"].ID,
		LaptopID: laptop.ID,
		ChairID:  chair.ID,
		HQID:     hq.ID,
		WHID:     wh.ID,
	}
}

type fixtureIDs struct {
	PatID, SamID      bson.ObjectID
	LaptopID, ChairID bson.ObjectID
	HQID, WHID        bson.ObjectID
}

// baseRow returns a fully-valid row that satisfies all required fields and
// the laptop category's required custom field (warrantyUntil).
func baseRow(n int) models.ImportRow {
	return models.ImportRow{
		RowNum:                 n,
		PONumber:               "PO-100",
		SupplierName:           "Acme",
		OrderDate:              "2026-01-15",
		POResponsibleUserEmail: "pat@example.com",
		LineItemName:           "MacBook",
		CategorySlug:           "laptop",
		Quantity:               "1",
		HomeVenueCode:          "HQ",
		SpecFields:             map[string]string{"warrantyUntil": "2027-01-15"},
	}
}

// --- tests ---------------------------------------------------------------

func TestResolve_HappyPath(t *testing.T) {
	r, ids := fixture(t)
	rows := []models.ImportRow{baseRow(2), baseRow(3)}
	rows[1].LineItemName = "MacBook Air"

	resolved, errs := r.Resolve(context.Background(), rows, ResolveOptions{})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	if len(resolved) != 2 {
		t.Fatalf("expected 2 resolved rows, got %d", len(resolved))
	}
	if resolved[0].PO.ResponsibleUserID != ids.PatID {
		t.Errorf("PO responsible: want Pat, got %s", resolved[0].PO.ResponsibleUserID.Hex())
	}
	if resolved[0].LineItem.CategoryID != ids.LaptopID {
		t.Errorf("category: want laptop, got %s", resolved[0].LineItem.CategoryID.Hex())
	}
	if resolved[0].LineItem.HomeVenueID != ids.HQID {
		t.Errorf("home venue: want HQ, got %s", resolved[0].LineItem.HomeVenueID.Hex())
	}
}

func TestResolve_RejectsUnknownUser(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.POResponsibleUserEmail = "ghost@example.com"
	_, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if !hasFieldErr(errs, 2, "poResponsibleUserEmail") {
		t.Fatalf("expected poResponsibleUserEmail error, got %+v", errs)
	}
}

func TestResolve_RejectsInactiveUser(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.POResponsibleUserEmail = "old@example.com"
	_, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if !hasFieldErr(errs, 2, "poResponsibleUserEmail") {
		t.Fatalf("expected inactive-user error, got %+v", errs)
	}
}

func TestResolve_RejectsUnknownCategoryAndVenue(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.CategorySlug = "nope"
	row.HomeVenueCode = "ZZ"
	_, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if !hasFieldErr(errs, 2, "categorySlug") || !hasFieldErr(errs, 2, "homeVenueCode") {
		t.Errorf("expected category + venue errors, got %+v", errs)
	}
}

func TestResolve_CategoryFallsBackToName(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.CategorySlug = "Office Chair" // name, not slug
	row.SpecFields = nil              // chair has no required spec
	_, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if len(errs) != 0 {
		t.Errorf("expected name-fallback to succeed, got: %+v", errs)
	}
}

func TestResolve_BadEnums(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.Status = "not-a-status"
	row.Condition = "shiny"
	_, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if !hasFieldErr(errs, 2, "status") || !hasFieldErr(errs, 2, "condition") {
		t.Errorf("expected status+condition errors, got %+v", errs)
	}
}

func TestResolve_BadDate(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.OrderDate = "yesterday"
	_, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if !hasFieldErr(errs, 2, "orderDate") {
		t.Errorf("expected orderDate error, got %+v", errs)
	}
}

func TestResolve_BadAssetTagFormat(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.AssetTag = "not_valid"
	_, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if !hasFieldErr(errs, 2, "assetTag") {
		t.Errorf("expected assetTag format error, got %+v", errs)
	}
}

func TestResolve_AssetTagAlreadyInDB(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.AssetTag = "LAP-0001" // pre-seeded as existing
	_, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if !hasFieldErr(errs, 2, "assetTag") {
		t.Errorf("expected assetTag-duplicate error, got %+v", errs)
	}
}

func TestResolve_DuplicateAssetTagInBatch(t *testing.T) {
	r, _ := fixture(t)
	r1 := baseRow(2)
	r1.AssetTag = "LAP-7000"
	r2 := baseRow(3)
	r2.AssetTag = "LAP-7000"
	_, errs := r.Resolve(context.Background(), []models.ImportRow{r1, r2}, ResolveOptions{})
	if !hasFieldErr(errs, 3, "assetTag") {
		t.Errorf("expected batch-duplicate error on row 3, got %+v", errs)
	}
}

func TestResolve_QuantityWithPerAssetOverride(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.Quantity = "5"
	row.AssetTag = "LAP-9000"
	_, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if !hasFieldErr(errs, 2, "quantity") {
		t.Errorf("expected quantity-vs-override error, got %+v", errs)
	}
}

func TestResolve_POConflictDefaultErrors(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.PONumber = "PO-EXISTS"
	resolved, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if !hasFieldErr(errs, 2, "poNumber") {
		t.Errorf("expected poNumber-conflict error, got %+v", errs)
	}
	if len(resolved) != 0 {
		t.Errorf("expected no resolved rows when erroring on conflict, got %d", len(resolved))
	}
}

func TestResolve_POConflictSkipExisting(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.PONumber = "PO-EXISTS"
	resolved, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{OnConflict: models.SkipExisting})
	if len(errs) != 0 {
		t.Errorf("expected no errors with skipExisting, got %+v", errs)
	}
	if len(resolved) != 1 || !resolved[0].SkipExisting {
		t.Errorf("expected 1 resolved row marked SkipExisting, got %+v", resolved)
	}
}

func TestResolve_POHeaderMismatchAcrossRows(t *testing.T) {
	r, _ := fixture(t)
	r1 := baseRow(2)
	r2 := baseRow(3)
	r2.SupplierName = "Different"
	_, errs := r.Resolve(context.Background(), []models.ImportRow{r1, r2}, ResolveOptions{})
	if !hasFieldErr(errs, 3, "poNumber") {
		t.Errorf("expected header-mismatch error on row 3, got %+v", errs)
	}
}

func TestResolve_LineGroupModeMismatch(t *testing.T) {
	r, _ := fixture(t)
	// Use the chair category so we don't trip the laptop's required spec.
	r1 := baseRow(2) // qty 1 + assetTag => modePerAsset
	r1.CategorySlug = "chair"
	r1.LineItemName = "Office Chair"
	r1.SpecFields = nil
	r1.AssetTag = "LAP-8001"
	r2 := baseRow(3) // qty 5 + no overrides => modeDefaults
	r2.CategorySlug = "chair"
	r2.LineItemName = "Office Chair"
	r2.SpecFields = nil
	r2.Quantity = "5"
	_, errs := r.Resolve(context.Background(), []models.ImportRow{r1, r2}, ResolveOptions{})
	if !hasFieldErr(errs, 3, "quantity") {
		t.Errorf("expected line-mode mismatch on row 3, got %+v", errs)
	}
}

func TestResolve_RequiredSpecMissing(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.SpecFields = nil // laptop requires warrantyUntil
	_, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if !hasFieldErr(errs, 2, "spec:warrantyUntil") {
		t.Errorf("expected missing required spec, got %+v", errs)
	}
}

func TestResolve_UnknownSpecKey(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.SpecFields["mystery"] = "value"
	_, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if !hasFieldErr(errs, 2, "spec:mystery") {
		t.Errorf("expected unknown-spec error, got %+v", errs)
	}
}

func TestResolve_SpecTypeCoercion(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.SpecFields["ram"] = "not-a-number"
	_, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if !hasFieldErr(errs, 2, "spec:ram") {
		t.Errorf("expected ram-cast error, got %+v", errs)
	}
}

func TestResolve_BooleanSpecCoercion(t *testing.T) {
	r, _ := fixture(t)
	row := baseRow(2)
	row.SpecFields["isPro"] = "yes"
	resolved, errs := r.Resolve(context.Background(), []models.ImportRow{row}, ResolveOptions{})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	if resolved[0].Override.Specs == nil || (*resolved[0].Override.Specs)["isPro"] != true {
		t.Errorf("expected isPro=true, got %+v", resolved[0].Override.Specs)
	}
}

func TestResolve_UnknownDepartmentCodeRejectsRow(t *testing.T) {
	r, ids := fixture(t)
	rows := []models.ImportRow{
		{
			RowNum:                 2,
			PONumber:               "PO-100",
			SupplierName:           "Acme",
			OrderDate:              "2026-01-15",
			POResponsibleUserEmail: "pat@example.com",
			LineItemName:           "MacBook",
			CategorySlug:           "laptop",
			HomeVenueCode:          "HQ",
			DepartmentCode:         "NOPE",
			Quantity:               "1",
		},
	}
	_, errs := r.Resolve(context.Background(), rows, ResolveOptions{})
	if len(errs) == 0 {
		t.Fatalf("want row error, got none")
	}
	found := false
	for _, e := range errs {
		if e.Field != nil && *e.Field == "departmentCode" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want departmentCode error, got %+v", errs)
	}
	_ = ids
}

func TestResolve_DepartmentCodeResolvesWithinRowVenue(t *testing.T) {
	// Extend the fixture: two venues, each with a department using the same code.
	r, ids := fixture(t)
	// Register two departments: HQ:ENG and WH:ENG (same code, different venues).
	engHQ := &models.Department{ID: bson.NewObjectID(), VenueID: ids.HQID, Code: "ENG"}
	engWH := &models.Department{ID: bson.NewObjectID(), VenueID: ids.WHID, Code: "ENG"}
	f := r.departments.(*fakeDepartments)
	f.byVenueAndCode[ids.HQID.Hex()+"|ENG"] = engHQ
	f.byVenueAndCode[ids.WHID.Hex()+"|ENG"] = engWH

	rows := []models.ImportRow{
		{
			RowNum: 2, PONumber: "PO-200", SupplierName: "Acme",
			OrderDate: "2026-01-15", POResponsibleUserEmail: "pat@example.com",
			LineItemName: "Laptop", CategorySlug: "laptop",
			HomeVenueCode: "HQ", DepartmentCode: "ENG", Quantity: "1",
			SpecFields: map[string]string{"warrantyUntil": "2027-01-15"},
		},
	}
	resolved, errs := r.Resolve(context.Background(), rows, ResolveOptions{})
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	if len(resolved) != 1 {
		t.Fatalf("want 1 resolved row, got %d", len(resolved))
	}
	got := resolved[0].Override.DepartmentID
	if got == nil || *got != engHQ.ID {
		t.Fatalf("want ENG@HQ, got %v", got)
	}
}

func hasFieldErr(errs []models.ImportRowError, row int, field string) bool {
	for _, e := range errs {
		if e.Row == row && e.Field != nil && *e.Field == field {
			return true
		}
	}
	return false
}
