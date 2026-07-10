package service

import (
	"context"
	"io"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/repository"
)

// ImportService orchestrates the bulk PO import pipeline (PRD §6.12).
//
//	Validate -> ImportJob{status: preview_ready}  (writes nothing else)
//	Commit   -> one Mongo transaction per PO, asset generation via
//	            PurchaseOrderService.ReceiveWithOptions (suppress=true)
//
// The job document carries the parsed rows so Commit can re-validate without
// re-parsing the original file.
type ImportService struct {
	jobs        *repository.ImportJobRepository
	posSvc      *PurchaseOrderService
	pos         *repository.PurchaseOrderRepository
	resolver    *Resolver
	users       *repository.UserRepository
	venues      *repository.VenueRepository
	categories  *repository.CategoryRepository
	departments *repository.DepartmentRepository
}

func NewImportService(
	jobs *repository.ImportJobRepository,
	posSvc *PurchaseOrderService,
	pos *repository.PurchaseOrderRepository,
	assets *repository.AssetRepository,
	users *repository.UserRepository,
	venues *repository.VenueRepository,
	categories *repository.CategoryRepository,
	departments *repository.DepartmentRepository,
) *ImportService {
	return &ImportService{
		jobs:        jobs,
		posSvc:      posSvc,
		pos:         pos,
		resolver:    NewResolver(users, categories, venues, assets, pos, departments),
		users:       users,
		venues:      venues,
		categories:  categories,
		departments: departments,
	}
}

// Validate parses, resolves, and persists a preview-ready ImportJob. Writes
// neither POs nor assets. Always returns the job document (even on errors)
// so the admin can download the row-level error list.
func (s *ImportService) Validate(
	ctx context.Context,
	filename string,
	body io.Reader,
	sizeHint int64,
	opts models.ImportJobOptions,
	uploadedBy bson.ObjectID,
) (*models.ImportPreview, error) {
	rows, err := ParseFromMultipart(filename, body, sizeHint)
	if err != nil {
		return nil, err
	}

	// Seed the job before resolving so we always have a record of the upload,
	// even if validation crashes mid-way.
	doc := &repository.ImportJobDoc{
		ImportJob: models.ImportJob{
			Filename:   filename,
			UploadedBy: uploadedBy,
			Status:     models.ImportJobStatusValidating,
			Options:    opts,
			Counts:     models.ImportJobCounts{},
			Errors:     []models.ImportRowError{},
		},
	}
	if err := s.jobs.Create(ctx, doc); err != nil {
		return nil, err
	}

	resolved, rowErrs := s.resolver.Resolve(ctx, rows, ResolveOptions{OnConflict: derefConflict(opts.OnConflict)})

	counts := computePreviewCounts(resolved, rowErrs)
	if rowErrs == nil {
		rowErrs = []models.ImportRowError{}
	}
	updated, err := s.jobs.Update(ctx, doc.ID, bson.M{
		"status": models.ImportJobStatusPreviewReady,
		"counts": counts,
		"errors": rowErrs,
	})
	if err != nil {
		return nil, err
	}
	if err := s.jobs.SaveParsedRows(ctx, doc.ID, rows); err != nil {
		return nil, err
	}

	return &models.ImportPreview{
		Job:        updated.ImportJob,
		PosPreview: buildPosPreview(resolved),
	}, nil
}

// Commit re-resolves the stored rows and creates POs + assets, one Mongo
// transaction per PO. Notifications are suppressed throughout. A failed PO
// is recorded as a row-level error and does not abort the run.
func (s *ImportService) Commit(
	ctx context.Context,
	jobID bson.ObjectID,
	opts models.ImportJobOptions,
	performedBy bson.ObjectID,
) (*models.ImportJob, error) {
	doc, err := s.jobs.FindByID(ctx, jobID)
	if err != nil {
		return nil, err
	}
	switch doc.Status {
	case models.ImportJobStatusPreviewReady:
		// happy path
	case models.ImportJobStatusCompleted, models.ImportJobStatusFailed, models.ImportJobStatusRolledBack:
		return nil, apperror.Conflict("import job is already finalized")
	case models.ImportJobStatusImporting:
		return nil, apperror.Conflict("import job is already running")
	default:
		return nil, apperror.Conflict("import job is not preview_ready")
	}

	// Re-resolve so we catch any DB changes since validate.
	resolved, rowErrs := s.resolver.Resolve(ctx, doc.ParsedRows, ResolveOptions{OnConflict: derefConflict(opts.OnConflict)})

	importValidOnly := opts.ImportValidOnly != nil && *opts.ImportValidOnly
	if len(rowErrs) > 0 && !importValidOnly {
		// Reject the whole commit unless the caller opted in.
		_, err := s.jobs.Update(ctx, jobID, bson.M{
			"status": models.ImportJobStatusPreviewReady,
			"errors": rowErrs,
		})
		if err != nil {
			return nil, err
		}
		return nil, apperror.BadRequest("import has row errors; fix or set importValidOnly=true to skip them")
	}

	if _, err := s.jobs.Update(ctx, jobID, bson.M{
		"status": models.ImportJobStatusImporting,
		"errors": rowErrs,
		"counts": models.ImportJobCounts{
			PosTotal:    countDistinctPOs(resolved),
			RowsErrored: len(rowErrs),
		},
	}); err != nil {
		return nil, err
	}

	// Group rows by poNumber (preserving input order).
	groups := groupByPONumber(resolved)
	jobIDPtr := jobID
	performedByPtr := performedBy

	var posCreated, assetsCreated, rowsSkipped int
	for _, g := range groups {
		if g.skipExisting() {
			rowsSkipped += len(g.rows)
			continue
		}
		created, made, err := s.commitOnePO(ctx, g, jobIDPtr, performedByPtr)
		if err != nil {
			// Surface the error against the first row of the group so the
			// admin can find it. Do NOT abort — continue to the next PO.
			rowErr := makeRowError(g.firstRow(), "poNumber", err.Error())
			if appendErr := s.jobs.AppendErrors(ctx, jobID, []models.ImportRowError{rowErr}); appendErr != nil {
				return nil, appendErr
			}
			if incErr := s.jobs.IncrementCounts(ctx, jobID, repository.CountsDelta{RowsErrored: 1}); incErr != nil {
				return nil, incErr
			}
			continue
		}
		if created {
			posCreated++
			assetsCreated += made
			if err := s.jobs.IncrementCounts(ctx, jobID, repository.CountsDelta{
				PosCreated:    1,
				AssetsCreated: made,
			}); err != nil {
				return nil, err
			}
		}
	}
	if rowsSkipped > 0 {
		if err := s.jobs.IncrementCounts(ctx, jobID, repository.CountsDelta{RowsSkipped: rowsSkipped}); err != nil {
			return nil, err
		}
	}

	finalStatus := models.ImportJobStatusCompleted
	if posCreated == 0 && len(groups) > 0 && rowsSkipped == 0 {
		// Nothing landed despite groups present → mark failed so callers know
		// the run produced no inserts.
		finalStatus = models.ImportJobStatusFailed
	}
	now := time.Now().UTC()
	final, err := s.jobs.Update(ctx, jobID, bson.M{
		"status":      finalStatus,
		"completedAt": now,
	})
	if err != nil {
		return nil, err
	}
	return &final.ImportJob, nil
}

// commitOnePO runs a single PO through the receive path inside one Mongo
// transaction. Returns (created, assetsCreated, err). created=false means
// the PO group was skipped (SkipExisting).
func (s *ImportService) commitOnePO(
	ctx context.Context,
	g poGroup,
	jobID bson.ObjectID,
	performedBy bson.ObjectID,
) (bool, int, error) {
	// 1. Build the PO document from the first row's header.
	first := g.rows[0]
	po := &models.PurchaseOrder{
		PONumber:          first.PO.PONumber,
		Supplier:          models.PurchaseOrderSupplier{Name: first.PO.SupplierName},
		ResponsibleUserID: first.PO.ResponsibleUserID,
		OrderDate:         first.PO.OrderDate,
		Status:            models.Ordered,
		CreatedBy:         performedBy,
		ImportJobID:       &jobID,
	}
	if first.PO.SupplierContact != "" {
		c := first.PO.SupplierContact
		po.Supplier.Contact = &c
	}
	if first.PO.Notes != "" {
		n := first.PO.Notes
		po.Notes = &n
	}
	// Roll line items up by (categoryID, lineItemName), summing quantities
	// per group and collecting per-asset overrides in input order.
	po.LineItems, _ = rollupLineItems(g.rows)
	// Build the parallel overrides slice.
	overrides := buildOverrides(po.LineItems, g.rows)

	if err := s.pos.Create(ctx, po); err != nil {
		return false, 0, err
	}

	res, err := s.posSvc.ReceiveWithOptions(ctx, po.ID, ReceiveOptions{
		VenueID:               g.defaultVenueID(),
		SuppressNotifications: true,
		ImportJobID:           &jobID,
		PerformedBy:           &performedBy,
		Overrides:             overrides,
	})
	if err != nil {
		return false, 0, err
	}
	return true, len(res.GeneratedAssetIDs), nil
}

// --- helpers -------------------------------------------------------------

// poGroup is a single PO's worth of resolved rows. Rows preserve input order.
type poGroup struct {
	poNumber string
	rows     []ResolvedRow
}

func (g poGroup) firstRow() int { return g.rows[0].Row.RowNum }
func (g poGroup) skipExisting() bool {
	for _, r := range g.rows {
		if r.SkipExisting {
			return true
		}
	}
	return false
}
func (g poGroup) defaultVenueID() bson.ObjectID {
	// Use the first row's home venue as the PO default; per-asset overrides
	// can still point elsewhere via the per-line overrides slice.
	return g.rows[0].LineItem.HomeVenueID
}

func groupByPONumber(rows []ResolvedRow) []poGroup {
	order := []string{}
	byPO := map[string][]ResolvedRow{}
	for _, r := range rows {
		if _, ok := byPO[r.PO.PONumber]; !ok {
			order = append(order, r.PO.PONumber)
		}
		byPO[r.PO.PONumber] = append(byPO[r.PO.PONumber], r)
	}
	out := make([]poGroup, 0, len(order))
	for _, num := range order {
		out = append(out, poGroup{poNumber: num, rows: byPO[num]})
	}
	return out
}

func countDistinctPOs(rows []ResolvedRow) int {
	seen := map[string]struct{}{}
	for _, r := range rows {
		if r.SkipExisting {
			continue
		}
		seen[r.PO.PONumber] = struct{}{}
	}
	return len(seen)
}

func computePreviewCounts(resolved []ResolvedRow, errs []models.ImportRowError) models.ImportJobCounts {
	posTotal := countDistinctPOs(resolved)
	assetsTotal := 0
	skipped := 0
	for _, r := range resolved {
		if r.SkipExisting {
			skipped += r.Quantity
			continue
		}
		assetsTotal += r.Quantity
	}
	return models.ImportJobCounts{
		PosTotal:      posTotal,
		AssetsCreated: assetsTotal, // value at preview time = what WILL be created
		RowsSkipped:   skipped,
		RowsErrored:   len(errs),
	}
}

func buildPosPreview(resolved []ResolvedRow) []models.ImportPreviewPO {
	type bucket struct {
		lineItems  map[string]bool
		assets     int
		categories map[string]bool
		venues     map[string]bool
		skip       bool
	}
	byPO := map[string]*bucket{}
	order := []string{}
	for _, r := range resolved {
		b, ok := byPO[r.PO.PONumber]
		if !ok {
			b = &bucket{
				lineItems:  map[string]bool{},
				categories: map[string]bool{},
				venues:     map[string]bool{},
			}
			byPO[r.PO.PONumber] = b
			order = append(order, r.PO.PONumber)
		}
		key := r.LineItem.CategoryID.Hex() + "|" + strings.ToLower(r.LineItem.Name)
		b.lineItems[key] = true
		b.assets += r.Quantity
		b.categories[r.LineItem.CategorySlug] = true
		b.venues[r.LineItem.HomeVenueID.Hex()] = true
		if r.SkipExisting {
			b.skip = true
		}
	}
	out := make([]models.ImportPreviewPO, 0, len(order))
	for _, num := range order {
		b := byPO[num]
		cats := keys(b.categories)
		venues := keys(b.venues)
		skip := b.skip
		out = append(out, models.ImportPreviewPO{
			PoNumber:     num,
			LineItems:    len(b.lineItems),
			AssetCount:   b.assets,
			Categories:   &cats,
			Venues:       &venues,
			SkipExisting: &skip,
		})
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// rollupLineItems collapses rows in a PO group into PurchaseOrderLineItem[].
// Rows sharing (categoryID, lineItemName) merge with summed quantities.
// Returns the line items and the (categoryID, lineItemName) → line item index
// map so buildOverrides can attach per-asset overrides to the right slot.
func rollupLineItems(rows []ResolvedRow) ([]models.PurchaseOrderLineItem, map[string]int) {
	idx := map[string]int{}
	var out []models.PurchaseOrderLineItem
	for _, r := range rows {
		key := r.LineItem.CategoryID.Hex() + "|" + strings.ToLower(r.LineItem.Name)
		if i, ok := idx[key]; ok {
			out[i].Quantity += r.Quantity
			continue
		}
		idx[key] = len(out)
		out = append(out, models.PurchaseOrderLineItem{
			CategoryID: r.LineItem.CategoryID,
			Name:       r.LineItem.Name,
			Quantity:   r.Quantity,
		})
	}
	return out, idx
}

// buildOverrides constructs the index-aligned overrides slice that matches
// the PO's LineItems. For each line item we walk the rows that contributed
// to it in input order; if any row carries any per-asset override, we build
// a full PerUnit slice (size = quantity), else we leave the line's PerUnit
// empty (all defaults).
func buildOverrides(lineItems []models.PurchaseOrderLineItem, rows []ResolvedRow) []LineItemOverrides {
	if len(lineItems) == 0 {
		return nil
	}
	out := make([]LineItemOverrides, len(lineItems))

	// Reverse-index lookup: line-item key → idx.
	keyForRow := func(r ResolvedRow) string {
		return r.LineItem.CategoryID.Hex() + "|" + strings.ToLower(r.LineItem.Name)
	}
	idx := map[string]int{}
	for i, li := range lineItems {
		idx[li.CategoryID.Hex()+"|"+strings.ToLower(li.Name)] = i
	}

	// Group rows by line-item key, preserving order.
	type groupedRows struct {
		anyOverrides bool
		rows         []ResolvedRow
	}
	grouped := map[string]*groupedRows{}
	for _, r := range rows {
		k := keyForRow(r)
		g, ok := grouped[k]
		if !ok {
			g = &groupedRows{}
			grouped[k] = g
		}
		g.rows = append(g.rows, r)
		if !isZeroOverride(r.Override) {
			g.anyOverrides = true
		}
	}

	for k, g := range grouped {
		i := idx[k]
		if !g.anyOverrides {
			continue // leave PerUnit nil = all defaults
		}
		// In this mode, the resolver guarantees every row has quantity=1.
		pu := make([]AssetOverride, 0, len(g.rows))
		for _, r := range g.rows {
			pu = append(pu, r.Override)
		}
		out[i] = LineItemOverrides{PerUnit: pu}
	}
	return out
}

func isZeroOverride(o AssetOverride) bool {
	return o.AssetTag == nil && o.Status == nil && o.Condition == nil &&
		o.HomeVenueID == nil && o.CurrentVenueID == nil && o.DepartmentID == nil && o.ResponsibleUserID == nil &&
		o.SerialNumber == nil && o.PurchaseDate == nil && o.ExpectedReturnDate == nil &&
		o.Notes == nil && o.Specs == nil && o.Photos == nil
}

func derefConflict(p *models.ImportConflictPolicy) models.ImportConflictPolicy {
	if p == nil {
		return models.Error
	}
	return *p
}

// Get returns the public-facing ImportJob (without ParsedRows).
func (s *ImportService) Get(ctx context.Context, id bson.ObjectID) (*models.ImportJob, error) {
	doc, err := s.jobs.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return &doc.ImportJob, nil
}
