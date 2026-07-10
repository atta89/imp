package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/repository"
)

type PurchaseOrderService struct {
	pos         *repository.PurchaseOrderRepository
	assets      *repository.AssetRepository
	movements   *repository.MovementRepository
	counters    *repository.CounterRepository
	venues      *repository.VenueRepository
	categories  *repository.CategoryRepository
	users       *repository.UserRepository
	departments *repository.DepartmentRepository
	client      *mongo.Client
}

func NewPurchaseOrderService(
	pos *repository.PurchaseOrderRepository,
	assets *repository.AssetRepository,
	movements *repository.MovementRepository,
	counters *repository.CounterRepository,
	venues *repository.VenueRepository,
	categories *repository.CategoryRepository,
	users *repository.UserRepository,
	departments *repository.DepartmentRepository,
	client *mongo.Client,
) *PurchaseOrderService {
	return &PurchaseOrderService{
		pos:         pos,
		assets:      assets,
		movements:   movements,
		counters:    counters,
		venues:      venues,
		categories:  categories,
		users:       users,
		departments: departments,
		client:      client,
	}
}

type PurchaseOrderListQuery struct {
	Status      models.PurchaseOrderStatus
	Responsible *bson.ObjectID
}

// AssetOverride lets a caller pre-fill specific fields on an asset created by
// the receive flow. Used by the bulk import pipeline (PRD §6.12) where the
// uploaded spreadsheet may carry tags, custodians, conditions etc. for
// historical assets. nil pointers fall back to the receive default.
type AssetOverride struct {
	AssetTag           *string
	Status             *models.AssetStatus
	Condition          *models.AssetCondition
	HomeVenueID        *bson.ObjectID
	CurrentVenueID     *bson.ObjectID
	ResponsibleUserID  *bson.ObjectID
	SerialNumber       *string
	PurchaseDate       *time.Time
	ExpectedReturnDate *time.Time
	Notes              *string
	Specs              *map[string]interface{}
	Photos             *[]string
	DepartmentID       *bson.ObjectID
}

// LineItemOverrides carries one AssetOverride per unit of a PO line item.
// len(PerUnit) must be either 0 (apply defaults to all units) or == quantity.
type LineItemOverrides struct {
	PerUnit []AssetOverride
}

// ReceiveOptions extends the receive flow with the knobs the bulk importer
// needs: per-asset overrides, notification suppression (so a 1000-row import
// doesn't blast every custodian and blow past Gmail's daily quota), and a
// stamp for traceability/rollback.
type ReceiveOptions struct {
	// VenueID is the default home venue for any unit whose override doesn't
	// specify HomeVenueID. Required.
	VenueID bson.ObjectID

	// DepartmentID is the default home department for any unit whose override
	// doesn't specify one. Must belong to VenueID.
	DepartmentID *bson.ObjectID

	// SuppressNotifications, when true, gates any triggers.* calls in this
	// flow. Today the receive flow doesn't enqueue notifications, so this
	// is forward-protection — but it's wired and asserted in tests so the
	// guarantee survives future trigger additions.
	SuppressNotifications bool

	// ImportJobID, when set, is stamped on every created asset and the PO
	// for traceability/rollback. Also triggers a per-asset custody_change
	// movement with reason "import" (requires PerformedBy).
	ImportJobID *bson.ObjectID

	// PerformedBy is the actor recorded on import-created movements. Must
	// be set when ImportJobID is set.
	PerformedBy *bson.ObjectID

	// Overrides is index-aligned with the PO's LineItems slice. Missing
	// entries (shorter slice or zero-value LineItemOverrides) fall back to
	// defaults.
	Overrides []LineItemOverrides
}

// resolvedAsset is the fully-defaulted set of fields that becomes an Asset
// document. Extracted from buildAssetForUnit so it's pure and testable.
type resolvedAsset struct {
	AssetTag           string // empty means "auto-generate"
	Status             models.AssetStatus
	Condition          models.AssetCondition
	HomeVenueID        bson.ObjectID
	CurrentVenueID     bson.ObjectID
	ResponsibleUserID  *bson.ObjectID
	SerialNumber       *string
	PurchaseDate       *time.Time
	ExpectedReturnDate *time.Time
	Notes              *string
	Specs              *map[string]interface{}
	Photos             *[]string
	DepartmentID       *bson.ObjectID
}

// resolveAssetFields applies an AssetOverride on top of the receive defaults.
// Pure function — unit-tested without a DB.
func resolveAssetFields(opts ReceiveOptions, po *models.PurchaseOrder, receivedAt time.Time, ov AssetOverride) resolvedAsset {
	r := resolvedAsset{
		Status:            models.Available,
		Condition:         models.New,
		HomeVenueID:       opts.VenueID,
		ResponsibleUserID: &po.ResponsibleUserID,
		PurchaseDate:      &receivedAt,
	}
	if opts.DepartmentID != nil {
		r.DepartmentID = opts.DepartmentID
	}
	if ov.AssetTag != nil {
		r.AssetTag = strings.TrimSpace(*ov.AssetTag)
	}
	if ov.Status != nil {
		r.Status = *ov.Status
	}
	if ov.Condition != nil {
		r.Condition = *ov.Condition
	}
	if ov.HomeVenueID != nil {
		r.HomeVenueID = *ov.HomeVenueID
	}
	if ov.CurrentVenueID != nil {
		r.CurrentVenueID = *ov.CurrentVenueID
	} else {
		r.CurrentVenueID = r.HomeVenueID
	}
	if ov.ResponsibleUserID != nil {
		r.ResponsibleUserID = ov.ResponsibleUserID
	}
	if ov.SerialNumber != nil {
		r.SerialNumber = ov.SerialNumber
	}
	if ov.PurchaseDate != nil {
		r.PurchaseDate = ov.PurchaseDate
	}
	if ov.ExpectedReturnDate != nil {
		r.ExpectedReturnDate = ov.ExpectedReturnDate
	}
	if ov.Notes != nil {
		r.Notes = ov.Notes
	}
	if ov.Specs != nil {
		r.Specs = ov.Specs
	}
	if ov.Photos != nil {
		r.Photos = ov.Photos
	}
	if ov.DepartmentID != nil {
		r.DepartmentID = ov.DepartmentID
	}
	return r
}

// tagPool hands out pre-reserved auto-generated asset tags per prefix. The
// reservation (one $inc:{seq:n} per prefix) happens up front in
// ReceiveWithOptions; take() just draws from the reserved block, so building
// N assets costs zero counter round-trips.
type tagPool struct {
	next map[string]int64
}

func newTagPool(firstSeq map[string]int64) *tagPool {
	n := make(map[string]int64, len(firstSeq))
	for k, v := range firstSeq {
		n[k] = v
	}
	return &tagPool{next: n}
}

func (p *tagPool) take(prefix string) string {
	seq := p.next[prefix]
	p.next[prefix] = seq + 1
	return fmt.Sprintf("%s-%04d", prefix, seq)
}

// deptPair is a (department, home venue) combination that needs the
// department-belongs-to-venue invariant checked once.
type deptPair struct {
	deptID  bson.ObjectID
	venueID bson.ObjectID
}

// receivePlan is the pre-transaction summary of a receive: how many
// auto-generated tags each prefix needs, which caller-supplied tags to check
// for collisions, and the distinct department/venue pairs to validate. It lets
// ReceiveWithOptions do a fixed, small number of DB round-trips regardless of
// how many assets the PO produces.
type receivePlan struct {
	autoTagCounts map[string]int
	overrideTags  []string
	deptPairs     map[string]deptPair
}

// collectReceivePlan walks every unit once (pure) to build the receivePlan. It
// mirrors buildReceiveDocs' iteration exactly (same override indexing, same
// resolveAssetFields), so the reserved tag blocks line up with what
// buildReceiveDocs later hands out.
func collectReceivePlan(po *models.PurchaseOrder, opts ReceiveOptions, cats map[bson.ObjectID]*models.Category, receivedAt time.Time) receivePlan {
	plan := receivePlan{
		autoTagCounts: map[string]int{},
		deptPairs:     map[string]deptPair{},
	}
	for liIdx, li := range po.LineItems {
		cat := cats[li.CategoryID]
		lineOv := lineItemOverridesAt(opts.Overrides, liIdx)
		for unit := 0; unit < li.Quantity; unit++ {
			ov := assetOverrideAt(lineOv, unit)
			rf := resolveAssetFields(opts, po, receivedAt, ov)
			if rf.AssetTag == "" {
				plan.autoTagCounts[assetTagPrefix(cat.Slug)]++
			} else {
				plan.overrideTags = append(plan.overrideTags, rf.AssetTag)
			}
			if rf.DepartmentID != nil {
				key := rf.DepartmentID.Hex() + "|" + rf.HomeVenueID.Hex()
				plan.deptPairs[key] = deptPair{deptID: *rf.DepartmentID, venueID: rf.HomeVenueID}
			}
		}
	}
	return plan
}

// buildReceiveDocs is the pure planning step behind ReceiveWithOptions: it
// turns a PO's line items (+ overrides) into the exact slices of asset and
// movement documents to bulk-insert. IDs are assigned here so movements can
// reference their asset, and so the caller can return GeneratedAssetIDs without
// re-reading. No DB access — all round-trips (tag reservation, uniqueness,
// department validation) are done by the caller before this runs.
func buildReceiveDocs(
	po *models.PurchaseOrder,
	opts ReceiveOptions,
	cats map[bson.ObjectID]*models.Category,
	receivedAt time.Time,
	tags *tagPool,
) ([]*models.Asset, []*models.Movement, error) {
	var totalUnits int
	for _, li := range po.LineItems {
		totalUnits += li.Quantity
	}
	assets := make([]*models.Asset, 0, totalUnits)
	var movements []*models.Movement

	for liIdx, li := range po.LineItems {
		cat := cats[li.CategoryID]
		lineOv := lineItemOverridesAt(opts.Overrides, liIdx)
		for unit := 0; unit < li.Quantity; unit++ {
			ov := assetOverrideAt(lineOv, unit)
			rf := resolveAssetFields(opts, po, receivedAt, ov)

			tag := rf.AssetTag
			if tag == "" {
				tag = tags.take(assetTagPrefix(cat.Slug))
			}
			token, err := generateQRToken()
			if err != nil {
				return nil, nil, apperror.Internal("generate qr token", err)
			}
			asset := &models.Asset{
				ID:                 bson.NewObjectID(),
				AssetTag:           tag,
				QrToken:            token,
				Name:               li.Name,
				CategoryID:         li.CategoryID,
				HomeVenueID:        rf.HomeVenueID,
				CurrentVenueID:     rf.CurrentVenueID,
				Status:             rf.Status,
				Condition:          rf.Condition,
				ResponsibleUserID:  rf.ResponsibleUserID,
				DepartmentID:       rf.DepartmentID,
				PurchaseOrderID:    &po.ID,
				PurchaseDate:       rf.PurchaseDate,
				SerialNumber:       rf.SerialNumber,
				Specs:              rf.Specs,
				Photos:             rf.Photos,
				ExpectedReturnDate: rf.ExpectedReturnDate,
				Notes:              rf.Notes,
				ImportJobID:        opts.ImportJobID,
				IsOverdue:          false,
				IsActive:           true,
			}
			assets = append(assets, asset)

			// Import-only audit movement: who got accountable for what,
			// stamped at import time. Reuses custody_change rather than
			// adding a new MovementType (PRD §6.12 plan note).
			if opts.ImportJobID != nil && rf.ResponsibleUserID != nil {
				reason := "import"
				movements = append(movements, &models.Movement{
					AssetID:     asset.ID,
					Type:        models.MovementTypeCustodyChange,
					ToUserID:    rf.ResponsibleUserID,
					Reason:      &reason,
					PerformedBy: *opts.PerformedBy,
				})
			}
		}
	}
	return assets, movements, nil
}

func (s *PurchaseOrderService) Create(ctx context.Context, in models.CreatePurchaseOrderRequest, createdBy bson.ObjectID) (*models.PurchaseOrder, error) {
	if strings.TrimSpace(in.PONumber) == "" {
		return nil, apperror.BadRequest("poNumber is required")
	}
	if strings.TrimSpace(in.Supplier.Name) == "" {
		return nil, apperror.BadRequest("supplier.name is required")
	}
	if len(in.LineItems) == 0 {
		return nil, apperror.BadRequest("at least one line item is required")
	}
	for i, li := range in.LineItems {
		if li.Quantity < 1 {
			return nil, apperror.BadRequest("line item quantity must be >= 1")
		}
		if strings.TrimSpace(li.Name) == "" {
			return nil, apperror.BadRequest("line item name is required")
		}
		if _, err := s.categories.FindByID(ctx, li.CategoryID); err != nil {
			return nil, apperror.BadRequest("line item " + itoa(i) + ": category not found")
		}
	}
	if _, err := s.users.FindByID(ctx, in.ResponsibleUserID); err != nil {
		return nil, apperror.BadRequest("responsible user not found")
	}

	po := &models.PurchaseOrder{
		PONumber:          strings.TrimSpace(in.PONumber),
		Supplier:          in.Supplier,
		ResponsibleUserID: in.ResponsibleUserID,
		OrderDate:         in.OrderDate,
		Status:            models.Draft,
		LineItems:         in.LineItems,
		Attachments:       in.Attachments,
		Notes:             in.Notes,
		CreatedBy:         createdBy,
	}
	if err := s.pos.Create(ctx, po); err != nil {
		return nil, err
	}
	return po, nil
}

func (s *PurchaseOrderService) Get(ctx context.Context, id bson.ObjectID) (*models.PurchaseOrder, error) {
	return s.pos.FindByID(ctx, id)
}

func (s *PurchaseOrderService) List(ctx context.Context, q PurchaseOrderListQuery, page, limit int) ([]models.PurchaseOrder, int64, error) {
	filter := bson.M{}
	if q.Status != "" {
		filter["status"] = q.Status
	}
	if q.Responsible != nil {
		filter["responsibleUserId"] = *q.Responsible
	}
	return s.pos.List(ctx, filter, page, limit)
}

// Update is a partial update for editable POs. A received PO is locked; a
// cancelled PO can still have its notes updated. Status can be moved to
// "ordered" or "cancelled" via this endpoint, but never to "received" — that
// must go through Receive so assets are generated atomically.
func (s *PurchaseOrderService) Update(ctx context.Context, id bson.ObjectID, in models.UpdatePurchaseOrderRequest) (*models.PurchaseOrder, error) {
	existing, err := s.pos.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing.Status == models.Received {
		return nil, apperror.Conflict("received purchase orders are read-only")
	}
	set := bson.M{}
	if in.PONumber != nil {
		if strings.TrimSpace(*in.PONumber) == "" {
			return nil, apperror.BadRequest("poNumber cannot be empty")
		}
		set["poNumber"] = strings.TrimSpace(*in.PONumber)
	}
	if in.Supplier != nil {
		if strings.TrimSpace(in.Supplier.Name) == "" {
			return nil, apperror.BadRequest("supplier.name is required")
		}
		set["supplier"] = *in.Supplier
	}
	if in.ResponsibleUserID != nil {
		if _, err := s.users.FindByID(ctx, *in.ResponsibleUserID); err != nil {
			return nil, apperror.BadRequest("responsible user not found")
		}
		set["responsibleUserId"] = *in.ResponsibleUserID
	}
	if in.OrderDate != nil {
		set["orderDate"] = *in.OrderDate
	}
	if in.LineItems != nil {
		if len(*in.LineItems) == 0 {
			return nil, apperror.BadRequest("at least one line item is required")
		}
		for _, li := range *in.LineItems {
			if li.Quantity < 1 {
				return nil, apperror.BadRequest("line item quantity must be >= 1")
			}
			if _, err := s.categories.FindByID(ctx, li.CategoryID); err != nil {
				return nil, apperror.BadRequest("line item category not found")
			}
		}
		set["lineItems"] = *in.LineItems
	}
	if in.Attachments != nil {
		set["attachments"] = *in.Attachments
	}
	if in.Notes != nil {
		set["notes"] = *in.Notes
	}
	if in.Status != nil {
		if *in.Status == models.Received {
			return nil, apperror.BadRequest("set status via POST /receive, not PUT")
		}
		if !validPOStatus(*in.Status) {
			return nil, apperror.BadRequest("invalid status")
		}
		set["status"] = *in.Status
	}
	if len(set) == 0 {
		return existing, nil
	}
	return s.pos.Update(ctx, id, set)
}

// Receive is the public PO-receive endpoint. Thin wrapper around
// ReceiveWithOptions with no overrides, no suppression, no import stamp —
// preserves the original behavior at /purchase-orders/:id/receive.
func (s *PurchaseOrderService) Receive(ctx context.Context, id bson.ObjectID, in models.ReceivePurchaseOrderRequest) (*models.ReceivePurchaseOrderResponse, error) {
	return s.ReceiveWithOptions(ctx, id, ReceiveOptions{VenueID: in.VenueID, DepartmentID: in.DepartmentID})
}

// ReceiveWithOptions atomically generates one asset per unit per line item and
// flips the PO to `received`. All inserts run inside a single Mongo
// transaction — either all succeed or none do (PRD §6.7).
//
// Compared to Receive: accepts per-asset overrides, can suppress
// notifications, and can stamp importJobId + audit movements on every asset
// it creates (used by the bulk import pipeline, PRD §6.12).
func (s *PurchaseOrderService) ReceiveWithOptions(ctx context.Context, id bson.ObjectID, opts ReceiveOptions) (*models.ReceivePurchaseOrderResponse, error) {
	po, err := s.pos.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	switch po.Status {
	case models.Received:
		return nil, apperror.Conflict("purchase order is already received")
	case models.Cancelled:
		return nil, apperror.Conflict("cancelled purchase order cannot be received")
	}
	if _, err := s.venues.FindByID(ctx, opts.VenueID); err != nil {
		return nil, apperror.BadRequest("venue not found")
	}
	if err := ValidateAssetDepartment(ctx, s.departments, opts.DepartmentID, opts.VenueID); err != nil {
		return nil, err
	}
	if opts.ImportJobID != nil && opts.PerformedBy == nil {
		return nil, apperror.BadRequest("importJobId requires performedBy")
	}
	// Validate categories up front so we don't start a transaction that's
	// destined to abort on the first line item.
	cats := make(map[bson.ObjectID]*models.Category, len(po.LineItems))
	for _, li := range po.LineItems {
		if _, ok := cats[li.CategoryID]; ok {
			continue
		}
		cat, err := s.categories.FindByID(ctx, li.CategoryID)
		if err != nil {
			return nil, apperror.BadRequest("line item category not found")
		}
		cats[li.CategoryID] = cat
	}
	// Validate overrides slice length matches per-line quantity when present.
	for i, li := range po.LineItems {
		ov := lineItemOverridesAt(opts.Overrides, i)
		if len(ov.PerUnit) != 0 && len(ov.PerUnit) != li.Quantity {
			return nil, apperror.BadRequest("override count mismatch for line item " + itoa(i))
		}
	}

	receivedAt := time.Now().UTC()

	// Plan the whole receive up front (pure) so the per-asset DB work — which
	// used to be N sequential round-trips inside one transaction (department
	// lookup + tag mint/uniqueness + asset insert + movement insert) and blew
	// past MongoDB's 60s transaction lifetime limit around ~1000 assets — is
	// collapsed into a handful of batched calls before we open the transaction.
	plan := collectReceivePlan(po, opts, cats, receivedAt)

	// 1. Validate each distinct (department, home venue) pair once instead of
	//    re-checking the same pair for every unit.
	for _, dp := range plan.deptPairs {
		deptID := dp.deptID
		if err := ValidateAssetDepartment(ctx, s.departments, &deptID, dp.venueID); err != nil {
			return nil, err
		}
	}

	// 2. Check all caller-supplied (override) tags for collisions in one query.
	if len(plan.overrideTags) > 0 {
		existing, err := s.assets.ExistsByTags(ctx, plan.overrideTags)
		if err != nil {
			return nil, err
		}
		if len(existing) > 0 {
			return nil, apperror.Conflict("asset tag already exists: " + existing[0])
		}
	}

	// 3. Reserve one contiguous block of tag sequence numbers per prefix (one
	//    $inc each) rather than one counter round-trip per asset.
	firstSeq := make(map[string]int64, len(plan.autoTagCounts))
	for prefix, count := range plan.autoTagCounts {
		first, err := s.counters.NextN(ctx, prefix, int64(count))
		if err != nil {
			return nil, err
		}
		firstSeq[prefix] = first
	}

	// 4. Build every asset + movement document in memory (assigns IDs so
	//    movements can reference their asset).
	assets, movements, err := buildReceiveDocs(po, opts, cats, receivedAt, newTagPool(firstSeq))
	if err != nil {
		return nil, err
	}

	// 5. Write it all in one transaction: bulk-insert assets, bulk-insert
	//    movements, flip the PO. On a transient abort WithTransaction retries
	//    the callback; IDs/tags were fixed above so the retry re-inserts the
	//    same documents after the aborted attempt rolled back.
	sess, err := s.client.StartSession()
	if err != nil {
		return nil, apperror.Internal("start session", err)
	}
	defer sess.EndSession(ctx)

	_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		if err := s.assets.InsertMany(sc, assets); err != nil {
			return nil, err
		}
		if err := s.movements.InsertMany(sc, movements); err != nil {
			return nil, err
		}
		set := bson.M{
			"status":       models.Received,
			"receivedDate": receivedAt,
		}
		if opts.ImportJobID != nil {
			set["importJobId"] = *opts.ImportJobID
		}
		if _, err := s.pos.Update(sc, id, set); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}

	generatedIDs := make([]bson.ObjectID, len(assets))
	for i, a := range assets {
		generatedIDs[i] = a.ID
	}
	updated, err := s.pos.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return &models.ReceivePurchaseOrderResponse{
		PurchaseOrder:     *updated,
		GeneratedAssetIDs: generatedIDs,
	}, nil
}

// lineItemOverridesAt returns the overrides for line item i, or a zero value
// if i is past the end of the slice (callers don't have to size the slice
// when most line items use defaults).
func lineItemOverridesAt(o []LineItemOverrides, i int) LineItemOverrides {
	if i >= 0 && i < len(o) {
		return o[i]
	}
	return LineItemOverrides{}
}

// assetOverrideAt returns the unit-level override at index unit, or a zero
// AssetOverride if PerUnit is empty (defaults-for-everything case).
func assetOverrideAt(line LineItemOverrides, unit int) AssetOverride {
	if unit >= 0 && unit < len(line.PerUnit) {
		return line.PerUnit[unit]
	}
	return AssetOverride{}
}

func validPOStatus(s models.PurchaseOrderStatus) bool {
	switch s {
	case models.Draft, models.Ordered, models.Received, models.Cancelled:
		return true
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
