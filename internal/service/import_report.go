package service

import (
	"context"
	"encoding/csv"
	"io"
	"sort"
	"strconv"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
	"imp/internal/repository"
)

// templateHeader is the column order written by RenderTemplate. Mirrors the
// columns the parser recognises (PRD §6.12). Per-asset override columns come
// after the required PO+line columns.
var templateHeader = []string{
	"poNumber",
	"supplierName",
	"supplierContact",
	"orderDate",
	"poNotes",
	"poResponsibleUserEmail",
	"lineItemName",
	"categorySlug",
	"quantity",
	"homeVenueCode",
	"departmentCode",
	"assetTag",
	"status",
	"condition",
	"currentVenueCode",
	"responsibleUserEmail",
	"serialNumber",
	"purchaseDate",
	"expectedReturnDate",
	"notes",
}

// templateExampleRow is a self-explanatory sample row paired with the header
// so non-technical admins can see the expected shape at a glance.
var templateExampleRow = []string{
	"PO-2026-001",           // poNumber
	"Acme Corp",             // supplierName
	"sales@acme.example",    // supplierContact (optional)
	"2026-01-15",            // orderDate (ISO-8601)
	"first quarterly batch", // poNotes (optional)
	"pat@example.com",       // poResponsibleUserEmail
	"MacBook Pro 14\"",      // lineItemName
	"laptop",                // categorySlug
	"1",                     // quantity (1 if using per-asset overrides)
	"HQ",                    // homeVenueCode
	"",                      // departmentCode (optional; blank → no department)
	"LAP-9001",              // assetTag (optional; blank → auto-generated)
	"available",             // status (optional; default available)
	"new",                   // condition (optional; default new)
	"HQ",                    // currentVenueCode (optional; default homeVenue)
	"pat@example.com",       // responsibleUserEmail (optional; default PO owner)
	"C02XXX",                // serialNumber (optional)
	"2026-01-15",            // purchaseDate (optional)
	"",                      // expectedReturnDate (optional)
	"",                      // notes (optional)
}

// RenderTemplate writes a CSV template to w: header row + one example data
// row. Spec:<key> columns are not included by default since they're
// category-dependent — the field guide in the PRD calls them out.
func RenderTemplate(w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(templateHeader); err != nil {
		return err
	}
	if err := cw.Write(templateExampleRow); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// CreatedPO + CreatedAsset are the report-row shapes the renderer expects.
// Wired by ImportService.Report from the database state of an import job.
type CreatedPO struct {
	PONumber string
	POID     bson.ObjectID
	Assets   []CreatedAsset
}

type CreatedAsset struct {
	AssetTag         string
	AssetID          bson.ObjectID
	CategorySlug     string
	HomeVenueCode    string
	CurrentVenueCode string
	ResponsibleEmail string
	Status           string
	Condition        string
}

// resultHeader is the column order for the per-asset success block.
var resultHeader = []string{
	"poNumber",
	"poId",
	"assetTag",
	"assetId",
	"categorySlug",
	"homeVenueCode",
	"currentVenueCode",
	"responsibleEmail",
	"status",
	"condition",
}

// RenderResult writes the post-commit CSV report: one row per created asset
// (grouped under their PO in input order), then a blank row, then a `#
// Errors` marker and the row-level error list.
func RenderResult(w io.Writer, created []CreatedPO, errs []models.ImportRowError) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(resultHeader); err != nil {
		return err
	}
	for _, po := range created {
		for _, a := range po.Assets {
			row := []string{
				po.PONumber,
				po.POID.Hex(),
				a.AssetTag,
				a.AssetID.Hex(),
				a.CategorySlug,
				a.HomeVenueCode,
				a.CurrentVenueCode,
				a.ResponsibleEmail,
				a.Status,
				a.Condition,
			}
			if err := cw.Write(row); err != nil {
				return err
			}
		}
	}
	if len(errs) > 0 {
		if err := cw.Write([]string{}); err != nil {
			return err
		}
		if err := cw.Write([]string{"# Errors"}); err != nil {
			return err
		}
		if err := cw.Write([]string{"row", "field", "message"}); err != nil {
			return err
		}
		for _, e := range errs {
			field := ""
			if e.Field != nil {
				field = *e.Field
			}
			if err := cw.Write([]string{strconv.Itoa(e.Row), field, e.Message}); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

// Report streams the result CSV for an ImportJob: looks up every PO + asset
// stamped with this importJobId and writes them in PO-grouped order.
func (s *ImportService) Report(ctx context.Context, jobID bson.ObjectID, assets *repository.AssetRepository, pos *repository.PurchaseOrderRepository, venues *repository.VenueRepository, categories *repository.CategoryRepository, users *repository.UserRepository, w io.Writer) error {
	doc, err := s.jobs.FindByID(ctx, jobID)
	if err != nil {
		return err
	}

	created, err := loadCreatedForJob(ctx, jobID, assets, pos, venues, categories, users)
	if err != nil {
		return err
	}
	return RenderResult(w, created, doc.Errors)
}

// loadCreatedForJob walks the POs and assets stamped with importJobId and
// builds the report-row shape, batching venue/user/category lookups so a
// 1000-asset report doesn't issue 4000 round-trips.
func loadCreatedForJob(
	ctx context.Context,
	jobID bson.ObjectID,
	assetsRepo *repository.AssetRepository,
	posRepo *repository.PurchaseOrderRepository,
	venuesRepo *repository.VenueRepository,
	categoriesRepo *repository.CategoryRepository,
	usersRepo *repository.UserRepository,
) ([]CreatedPO, error) {
	// Page through POs stamped with this jobID.
	poDocs, _, err := posRepo.List(ctx, bson.M{"importJobId": jobID}, 1, 1000)
	if err != nil {
		return nil, err
	}
	if len(poDocs) == 0 {
		return nil, nil
	}

	// Batch venue/category/user lookups across the asset set per PO.
	out := make([]CreatedPO, 0, len(poDocs))
	for _, po := range poDocs {
		assetDocs, _, err := assetsRepo.List(ctx, bson.M{
			"importJobId":     jobID,
			"purchaseOrderId": po.ID,
		}, 1, 1000)
		if err != nil {
			return nil, err
		}

		venueIDs := map[bson.ObjectID]struct{}{}
		catIDs := map[bson.ObjectID]struct{}{}
		userIDs := map[bson.ObjectID]struct{}{}
		for _, a := range assetDocs {
			venueIDs[a.HomeVenueID] = struct{}{}
			venueIDs[a.CurrentVenueID] = struct{}{}
			catIDs[a.CategoryID] = struct{}{}
			if a.ResponsibleUserID != nil {
				userIDs[*a.ResponsibleUserID] = struct{}{}
			}
		}

		venueByID := map[bson.ObjectID]string{}
		vList, err := venuesRepo.FindByIDs(ctx, keysOID(venueIDs))
		if err != nil {
			return nil, err
		}
		for _, v := range vList {
			venueByID[v.ID] = v.Code
		}

		catByID := map[bson.ObjectID]string{}
		for id := range catIDs {
			c, err := categoriesRepo.FindByID(ctx, id)
			if err == nil {
				catByID[id] = c.Slug
			}
		}

		userByID := map[bson.ObjectID]string{}
		uList, err := usersRepo.FindByIDs(ctx, keysOID(userIDs))
		if err != nil {
			return nil, err
		}
		for _, u := range uList {
			userByID[u.ID] = string(u.Email)
		}

		// Stable sort: by assetTag so the report is deterministic.
		sort.Slice(assetDocs, func(i, j int) bool { return assetDocs[i].AssetTag < assetDocs[j].AssetTag })

		assets := make([]CreatedAsset, 0, len(assetDocs))
		for _, a := range assetDocs {
			respEmail := ""
			if a.ResponsibleUserID != nil {
				respEmail = userByID[*a.ResponsibleUserID]
			}
			assets = append(assets, CreatedAsset{
				AssetTag:         a.AssetTag,
				AssetID:          a.ID,
				CategorySlug:     catByID[a.CategoryID],
				HomeVenueCode:    venueByID[a.HomeVenueID],
				CurrentVenueCode: venueByID[a.CurrentVenueID],
				ResponsibleEmail: respEmail,
				Status:           string(a.Status),
				Condition:        string(a.Condition),
			})
		}
		out = append(out, CreatedPO{
			PONumber: po.PONumber,
			POID:     po.ID,
			Assets:   assets,
		})
	}
	return out, nil
}

func keysOID(m map[bson.ObjectID]struct{}) []bson.ObjectID {
	out := make([]bson.ObjectID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
