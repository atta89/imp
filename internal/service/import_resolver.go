package service

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
)

// Small repository interfaces consumed by the resolver. The concrete repos
// satisfy these; tests use in-memory fakes so the resolver can be tested
// without Mongo.
type userFinder interface {
	FindByEmail(ctx context.Context, email string) (*models.User, error)
}
type categoryFinder interface {
	FindBySlug(ctx context.Context, slug string) (*models.Category, error)
	FindByName(ctx context.Context, name string) (*models.Category, error)
}
type venueFinder interface {
	FindByCode(ctx context.Context, code string) (*models.Venue, error)
}
type assetExister interface {
	ExistsByTag(ctx context.Context, tag string) (bool, error)
}
type poExister interface {
	ExistsByNumber(ctx context.Context, number string) (bool, error)
}
type departmentFinder interface {
	FindByVenueAndCode(ctx context.Context, venueID bson.ObjectID, code string) (*models.Department, error)
}

// Resolver validates parsed import rows against the live system. It returns
// one ResolvedRow per non-skipped row plus a flat list of per-row errors —
// the caller decides whether to surface them or commit.
type Resolver struct {
	users       userFinder
	categories  categoryFinder
	venues      venueFinder
	assets      assetExister
	pos         poExister
	departments departmentFinder
}

func NewResolver(users userFinder, categories categoryFinder, venues venueFinder, assets assetExister, pos poExister, departments departmentFinder) *Resolver {
	return &Resolver{
		users:       users,
		categories:  categories,
		venues:      venues,
		assets:      assets,
		pos:         pos,
		departments: departments,
	}
}

// ResolveOptions tunes resolver behavior — pulled from the ImportJob's
// stored ImportJobOptions plus a couple of internal flags.
type ResolveOptions struct {
	OnConflict models.ImportConflictPolicy
}

// ResolvedRow is the fully-resolved + validated form of a single ImportRow.
// SkipExisting=true means the row belongs to a poNumber that already exists
// in the DB and the caller asked to skip rather than error.
type ResolvedRow struct {
	Row          models.ImportRow
	PO           ResolvedPOHeader
	LineItem     ResolvedLineItem
	Override     AssetOverride // empty when all per-asset columns blank
	Quantity     int
	SkipExisting bool // entire PO group is skipped at commit time
}

type ResolvedPOHeader struct {
	PONumber          string
	SupplierName      string
	SupplierContact   string
	Notes             string
	OrderDate         time.Time
	ResponsibleUserID bson.ObjectID
}

type ResolvedLineItem struct {
	CategoryID   bson.ObjectID
	CategorySlug string
	Name         string
	HomeVenueID  bson.ObjectID
}

// assetTagPattern is the format an imported assetTag must match: 2-6
// uppercase letters/digits, a hyphen, then at least 4 digits. Matches the
// in-system auto-generated shape "LAP-0001".
var assetTagPattern = regexp.MustCompile(`^[A-Z0-9]{2,6}-\d{4,}$`)

// Resolve walks every row, validating against the live DB. Returns the
// resolved rows (for the rows that passed) and a flat error list. A row with
// errors does NOT appear in the resolved slice. Rows that belong to an
// existing poNumber are either reported as errors (default) or marked
// SkipExisting (when OnConflict=="skipExisting").
//
// Validation walks each row once; cross-row checks (per-batch duplicate tags,
// per-PO header consistency, mixed quantity+overrides within a line group)
// run after.
func (r *Resolver) Resolve(ctx context.Context, rows []models.ImportRow, opts ResolveOptions) ([]ResolvedRow, []models.ImportRowError) {
	var errs []models.ImportRowError
	resolved := make([]ResolvedRow, 0, len(rows))

	// Per-batch caches to avoid hammering the same lookup repeatedly. The
	// resolver is per-import — caching for the run's lifetime is safe.
	venueCache := map[string]*models.Venue{}
	catCache := map[string]*models.Category{}
	userCache := map[string]*models.User{}
	poExistCache := map[string]bool{}

	// Tracks an assetTag → first row it appeared on. A later row reusing the
	// same tag becomes an error against that later row.
	tagFirstSeen := map[string]int{}

	// PO header consistency: every row sharing a poNumber must carry the
	// same supplier/orderDate/responsibleEmail/notes. Track the first row's
	// values per poNumber.
	type headerSnap struct {
		row             int
		supplierName    string
		supplierContact string
		orderDate       string
		responsible     string
		notes           string
	}
	headerByPO := map[string]headerSnap{}

	// Per-line-item override-vs-quantity consistency: a (poNumber, category,
	// name) group either has every row with all per-asset columns blank, or
	// every row with quantity=1.
	type lineGroupMode int
	const (
		modeUnset lineGroupMode = iota
		modeDefaults
		modePerAsset
	)
	lineMode := map[string]lineGroupMode{}

	for _, row := range rows {
		rowErrs := r.validateRow(ctx, row, opts, venueCache, catCache, userCache, poExistCache)
		if len(rowErrs.fieldErrors) > 0 {
			for _, fe := range rowErrs.fieldErrors {
				errs = append(errs, makeRowError(row.RowNum, fe.field, fe.message))
			}
			continue
		}

		// Cross-row: PO header consistency.
		if prev, ok := headerByPO[row.PONumber]; ok {
			if prev.supplierName != row.SupplierName ||
				prev.supplierContact != row.SupplierContact ||
				prev.orderDate != row.OrderDate ||
				prev.responsible != strings.ToLower(row.POResponsibleUserEmail) ||
				prev.notes != row.PONotes {
				errs = append(errs, makeRowError(row.RowNum, "poNumber",
					"PO header fields disagree with row "+strconv.Itoa(prev.row)+" for same poNumber"))
				continue
			}
		} else {
			headerByPO[row.PONumber] = headerSnap{
				row:             row.RowNum,
				supplierName:    row.SupplierName,
				supplierContact: row.SupplierContact,
				orderDate:       row.OrderDate,
				responsible:     strings.ToLower(row.POResponsibleUserEmail),
				notes:           row.PONotes,
			}
		}

		// Cross-row: per-line-item group consistency.
		lineKey := row.PONumber + "|" + rowErrs.resolved.LineItem.CategoryID.Hex() + "|" + strings.ToLower(row.LineItemName)
		thisMode := modeDefaults
		if rowErrs.resolved.Quantity == 1 && hasAnyPerAssetOverride(row) {
			thisMode = modePerAsset
		}
		if existing, ok := lineMode[lineKey]; ok {
			if existing != thisMode {
				errs = append(errs, makeRowError(row.RowNum, "quantity",
					"rows for the same (poNumber, categorySlug, lineItemName) must all use defaults OR all use per-asset overrides — not a mix"))
				continue
			}
		} else {
			lineMode[lineKey] = thisMode
		}

		// Cross-row: batch-level duplicate assetTag.
		if row.AssetTag != "" {
			if first, dup := tagFirstSeen[row.AssetTag]; dup {
				errs = append(errs, makeRowError(row.RowNum, "assetTag",
					"duplicate assetTag in upload (first seen at row "+strconv.Itoa(first)+")"))
				continue
			}
			tagFirstSeen[row.AssetTag] = row.RowNum
		}

		// SkipExisting: poNumber-conflict policy.
		if rowErrs.poExists && opts.OnConflict == models.SkipExisting {
			rowErrs.resolved.SkipExisting = true
		}

		resolved = append(resolved, rowErrs.resolved)
	}
	return resolved, errs
}

// fieldErr is an internal one-row error tuple.
type fieldErr struct{ field, message string }

// makeRowError turns a (row, field, message) tuple into the public error type,
// translating the optional field string to a pointer.
func makeRowError(row int, field, message string) models.ImportRowError {
	e := models.ImportRowError{Row: row, Message: message}
	if field != "" {
		f := field
		e.Field = &f
	}
	return e
}

// rowValidation collects the result of a single-row validation pass.
type rowValidation struct {
	resolved    ResolvedRow
	fieldErrors []fieldErr
	poExists    bool
}

func (r *Resolver) validateRow(
	ctx context.Context,
	row models.ImportRow,
	opts ResolveOptions,
	venueCache map[string]*models.Venue,
	catCache map[string]*models.Category,
	userCache map[string]*models.User,
	poExistCache map[string]bool,
) rowValidation {
	var v rowValidation
	v.resolved.Row = row
	v.resolved.PO.PONumber = row.PONumber

	// Required cells.
	addRequired := func(field, value string) {
		if value == "" {
			v.fieldErrors = append(v.fieldErrors, fieldErr{field, field + " is required"})
		}
	}
	addRequired("poNumber", row.PONumber)
	addRequired("supplierName", row.SupplierName)
	addRequired("orderDate", row.OrderDate)
	addRequired("poResponsibleUserEmail", row.POResponsibleUserEmail)
	addRequired("lineItemName", row.LineItemName)
	addRequired("categorySlug", row.CategorySlug)
	addRequired("homeVenueCode", row.HomeVenueCode)

	// OrderDate format. Accept both date-only and full RFC3339.
	if row.OrderDate != "" {
		if t, err := parseFlexibleDate(row.OrderDate); err == nil {
			v.resolved.PO.OrderDate = t
		} else {
			v.fieldErrors = append(v.fieldErrors, fieldErr{"orderDate", "expected ISO-8601 (YYYY-MM-DD or RFC3339): " + err.Error()})
		}
	}

	v.resolved.PO.SupplierName = row.SupplierName
	v.resolved.PO.SupplierContact = row.SupplierContact
	v.resolved.PO.Notes = row.PONotes

	// PO-responsible user.
	if row.POResponsibleUserEmail != "" {
		u, err := lookupUser(ctx, r.users, userCache, row.POResponsibleUserEmail)
		if err != nil {
			v.fieldErrors = append(v.fieldErrors, fieldErr{"poResponsibleUserEmail", userLookupMsg(err)})
		} else {
			v.resolved.PO.ResponsibleUserID = u.ID
		}
	}

	// Category.
	if row.CategorySlug != "" {
		cat, err := lookupCategory(ctx, r.categories, catCache, row.CategorySlug)
		if err != nil {
			v.fieldErrors = append(v.fieldErrors, fieldErr{"categorySlug", "category not found by slug or name: " + row.CategorySlug})
		} else {
			v.resolved.LineItem.CategoryID = cat.ID
			v.resolved.LineItem.CategorySlug = cat.Slug
		}
	}
	v.resolved.LineItem.Name = row.LineItemName

	// Home venue.
	if row.HomeVenueCode != "" {
		ve, err := lookupVenue(ctx, r.venues, venueCache, row.HomeVenueCode)
		if err != nil {
			v.fieldErrors = append(v.fieldErrors, fieldErr{"homeVenueCode", "venue not found: " + row.HomeVenueCode})
		} else {
			v.resolved.LineItem.HomeVenueID = ve.ID
		}
	}

	// Quantity.
	qty := 1
	if row.Quantity != "" {
		n, err := strconv.Atoi(row.Quantity)
		switch {
		case err != nil:
			v.fieldErrors = append(v.fieldErrors, fieldErr{"quantity", "not a number: " + row.Quantity})
		case n < 1:
			v.fieldErrors = append(v.fieldErrors, fieldErr{"quantity", "must be >= 1"})
		default:
			qty = n
		}
	}
	v.resolved.Quantity = qty

	// Multi-asset row + per-asset override mix.
	if qty > 1 && hasAnyPerAssetOverride(row) {
		v.fieldErrors = append(v.fieldErrors, fieldErr{
			"quantity",
			"per-asset override on a multi-asset row; either set quantity=1 or leave per-asset columns blank",
		})
	}

	// Per-asset overrides — only validated when quantity=1, but enum/format
	// checks are useful either way.
	ov := AssetOverride{}
	if row.AssetTag != "" {
		clean := strings.ToUpper(strings.TrimSpace(row.AssetTag))
		if !assetTagPattern.MatchString(clean) {
			v.fieldErrors = append(v.fieldErrors, fieldErr{"assetTag", "format must match LETTERS-NUMBERS (e.g. LAP-0001)"})
		} else {
			exists, err := r.assets.ExistsByTag(ctx, clean)
			if err != nil {
				v.fieldErrors = append(v.fieldErrors, fieldErr{"assetTag", "tag-uniqueness lookup failed: " + err.Error()})
			} else if exists {
				v.fieldErrors = append(v.fieldErrors, fieldErr{"assetTag", "assetTag already in use: " + clean})
			} else {
				tag := clean
				ov.AssetTag = &tag
			}
		}
	}
	if row.Status != "" {
		if !validImportStatus(row.Status) {
			v.fieldErrors = append(v.fieldErrors, fieldErr{"status", "must be one of available, in_use, in_repair, retired, lost"})
		} else {
			s := models.AssetStatus(row.Status)
			ov.Status = &s
		}
	}
	if row.Condition != "" {
		if !validImportCondition(row.Condition) {
			v.fieldErrors = append(v.fieldErrors, fieldErr{"condition", "must be one of new, good, fair, poor"})
		} else {
			c := models.AssetCondition(row.Condition)
			ov.Condition = &c
		}
	}
	if row.CurrentVenueCode != "" {
		ve, err := lookupVenue(ctx, r.venues, venueCache, row.CurrentVenueCode)
		if err != nil {
			v.fieldErrors = append(v.fieldErrors, fieldErr{"currentVenueCode", "venue not found: " + row.CurrentVenueCode})
		} else {
			vid := ve.ID
			ov.CurrentVenueID = &vid
		}
	}
	if row.ResponsibleUserEmail != "" {
		u, err := lookupUser(ctx, r.users, userCache, row.ResponsibleUserEmail)
		if err != nil {
			v.fieldErrors = append(v.fieldErrors, fieldErr{"responsibleUserEmail", userLookupMsg(err)})
		} else {
			uid := u.ID
			ov.ResponsibleUserID = &uid
		}
	}
	if row.SerialNumber != "" {
		sn := row.SerialNumber
		ov.SerialNumber = &sn
	}
	if row.PurchaseDate != "" {
		if t, err := parseFlexibleDate(row.PurchaseDate); err == nil {
			ov.PurchaseDate = &t
		} else {
			v.fieldErrors = append(v.fieldErrors, fieldErr{"purchaseDate", "expected ISO-8601: " + err.Error()})
		}
	}
	if row.ExpectedReturnDate != "" {
		if t, err := parseFlexibleDate(row.ExpectedReturnDate); err == nil {
			ov.ExpectedReturnDate = &t
		} else {
			v.fieldErrors = append(v.fieldErrors, fieldErr{"expectedReturnDate", "expected ISO-8601: " + err.Error()})
		}
	}
	if row.Notes != "" {
		n := row.Notes
		ov.Notes = &n
	}

	// Department (scoped to the row's home venue).
	if row.DepartmentCode != "" {
		if v.resolved.LineItem.HomeVenueID.IsZero() {
			// Home venue failed above; skip department resolution to avoid a
			// noisy second error on the same row.
		} else {
			d, err := r.departments.FindByVenueAndCode(ctx, v.resolved.LineItem.HomeVenueID, row.DepartmentCode)
			if err != nil {
				if appErr, ok := apperror.As(err); ok && appErr.Kind == apperror.KindNotFound {
					v.fieldErrors = append(v.fieldErrors, fieldErr{"departmentCode", "department not found for this venue: " + row.DepartmentCode})
				} else {
					v.fieldErrors = append(v.fieldErrors, fieldErr{"departmentCode", "department lookup failed: " + err.Error()})
				}
			} else {
				id := d.ID
				ov.DepartmentID = &id
			}
		}
	}

	// Spec validation: run whenever we have a resolved category, even if no
	// spec columns are provided — that's how we catch missing required fields.
	if row.CategorySlug != "" {
		cat, _ := lookupCategory(ctx, r.categories, catCache, row.CategorySlug)
		if cat != nil {
			specs, specErrs := validateSpecFields(row.SpecFields, cat.CustomFields)
			v.fieldErrors = append(v.fieldErrors, specErrs...)
			if len(specs) > 0 {
				m := specs
				ov.Specs = &m
			}
		}
	}

	v.resolved.Override = ov

	// poNumber conflict against existing DB.
	if row.PONumber != "" {
		exists, ok := poExistCache[row.PONumber]
		if !ok {
			var err error
			exists, err = r.pos.ExistsByNumber(ctx, row.PONumber)
			if err != nil {
				v.fieldErrors = append(v.fieldErrors, fieldErr{"poNumber", "uniqueness lookup failed: " + err.Error()})
			} else {
				poExistCache[row.PONumber] = exists
			}
		}
		if exists {
			v.poExists = true
			if opts.OnConflict != models.SkipExisting {
				v.fieldErrors = append(v.fieldErrors, fieldErr{"poNumber", "PO with this number already exists; rerun with onConflict=skipExisting to skip"})
			}
		}
	}

	return v
}

// hasAnyPerAssetOverride reports whether ANY per-asset override column on the
// row is non-empty. Used to enforce the multi-asset row rule.
func hasAnyPerAssetOverride(row models.ImportRow) bool {
	return row.AssetTag != "" ||
		row.Status != "" ||
		row.Condition != "" ||
		row.CurrentVenueCode != "" ||
		row.DepartmentCode != "" ||
		row.ResponsibleUserEmail != "" ||
		row.SerialNumber != "" ||
		row.PurchaseDate != "" ||
		row.ExpectedReturnDate != "" ||
		row.Notes != "" ||
		len(row.SpecFields) > 0
}

func validImportStatus(s string) bool {
	switch models.AssetStatus(s) {
	case models.Available, models.InUse, models.InRepair, models.Retired, models.Lost:
		return true
	}
	return false
}

func validImportCondition(c string) bool {
	switch models.AssetCondition(c) {
	case models.New, models.Good, models.Fair, models.Poor:
		return true
	}
	return false
}

// parseFlexibleDate accepts "YYYY-MM-DD", "YYYY-MM-DDTHH:MM:SSZ", or any
// RFC3339 form. Returns UTC time.
func parseFlexibleDate(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("unrecognised date: " + s)
}

func lookupUser(ctx context.Context, finder userFinder, cache map[string]*models.User, email string) (*models.User, error) {
	key := strings.ToLower(strings.TrimSpace(email))
	if u, ok := cache[key]; ok {
		if u == nil {
			return nil, apperror.NotFound("user not found")
		}
		return u, nil
	}
	u, err := finder.FindByEmail(ctx, key)
	if err != nil {
		if appErr, ok := apperror.As(err); ok && appErr.Kind == apperror.KindNotFound {
			cache[key] = nil
		}
		return nil, err
	}
	if !u.IsActive {
		return nil, apperror.BadRequest("user is not active")
	}
	cache[key] = u
	return u, nil
}

func userLookupMsg(err error) string {
	if appErr, ok := apperror.As(err); ok {
		switch appErr.Kind {
		case apperror.KindNotFound:
			return "responsible user not found (do not auto-create)"
		case apperror.KindBadRequest:
			return appErr.Message
		}
	}
	return "user lookup failed: " + err.Error()
}

func lookupCategory(ctx context.Context, finder categoryFinder, cache map[string]*models.Category, key string) (*models.Category, error) {
	k := strings.ToLower(strings.TrimSpace(key))
	if c, ok := cache[k]; ok {
		if c == nil {
			return nil, apperror.NotFound("category not found")
		}
		return c, nil
	}
	c, err := finder.FindBySlug(ctx, k)
	if err == nil {
		cache[k] = c
		return c, nil
	}
	if appErr, ok := apperror.As(err); !ok || appErr.Kind != apperror.KindNotFound {
		return nil, err
	}
	// Slug miss → try name.
	c, err = finder.FindByName(ctx, key)
	if err != nil {
		if appErr, ok := apperror.As(err); ok && appErr.Kind == apperror.KindNotFound {
			cache[k] = nil
		}
		return nil, err
	}
	cache[k] = c
	return c, nil
}

func lookupVenue(ctx context.Context, finder venueFinder, cache map[string]*models.Venue, code string) (*models.Venue, error) {
	k := strings.TrimSpace(code)
	if v, ok := cache[k]; ok {
		if v == nil {
			return nil, apperror.NotFound("venue not found")
		}
		return v, nil
	}
	v, err := finder.FindByCode(ctx, k)
	if err != nil {
		if appErr, ok := apperror.As(err); ok && appErr.Kind == apperror.KindNotFound {
			cache[k] = nil
		}
		return nil, err
	}
	cache[k] = v
	return v, nil
}

// validateSpecFields casts each spec:* value against the category's
// CustomFields definition. Returns the typed map + per-field errors. Unknown
// keys are an error; missing required fields are an error.
func validateSpecFields(in map[string]string, defs []models.CategoryCustomField) (map[string]interface{}, []fieldErr) {
	out := make(map[string]interface{}, len(in))
	var errs []fieldErr
	defByKey := make(map[string]models.CategoryCustomField, len(defs))
	for _, d := range defs {
		defByKey[d.Key] = d
	}
	for key, raw := range in {
		def, ok := defByKey[key]
		if !ok {
			errs = append(errs, fieldErr{"spec:" + key, "unknown custom field for this category"})
			continue
		}
		val, err := castSpecValue(raw, def.Type)
		if err != nil {
			errs = append(errs, fieldErr{"spec:" + key, "value: " + err.Error()})
			continue
		}
		out[key] = val
	}
	// Required-field check.
	for _, d := range defs {
		if d.Required {
			if _, present := out[d.Key]; !present {
				errs = append(errs, fieldErr{"spec:" + d.Key, "required custom field is missing"})
			}
		}
	}
	return out, errs
}

func castSpecValue(raw string, t models.CustomFieldType) (interface{}, error) {
	switch t {
	case models.String:
		return raw, nil
	case models.Number:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, errors.New("not a number")
		}
		return f, nil
	case models.Boolean:
		switch strings.ToLower(raw) {
		case "true", "yes", "y", "1":
			return true, nil
		case "false", "no", "n", "0":
			return false, nil
		}
		return nil, errors.New("not a boolean (true/false/yes/no/1/0)")
	case models.Date:
		t, err := parseFlexibleDate(raw)
		if err != nil {
			return nil, err
		}
		return t, nil
	}
	return raw, nil
}
