package service

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"

	"imp/internal/apperror"
	"imp/internal/models"
)

// requiredImportHeaders are the columns every import file must include. Each
// data row needs a non-empty value in each of these (validated by the
// resolver, not the parser — the parser only checks the headers are present).
var requiredImportHeaders = []string{
	"poNumber",
	"supplierName",
	"orderDate",
	"poResponsibleUserEmail",
	"lineItemName",
	"categorySlug",
	"homeVenueCode",
}

// fixedImportColumns is the closed set of recognised column names (excluding
// `spec:<key>` which is open-ended). Headers outside this set + the spec
// prefix are silently ignored so users can keep extra notes-columns in their
// working sheet.
var fixedImportColumns = []string{
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

// ParseImportFile detects the format by extension and parses to []ImportRow.
// CSV uses the stdlib reader; XLSX uses excelize. Both tolerate a UTF-8 BOM
// on the first cell and strip surrounding whitespace per cell.
func ParseImportFile(filename string, r io.Reader) ([]models.ImportRow, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".csv":
		return parseCSVImport(r)
	case ".xlsx", ".xlsm":
		return parseXLSXImport(r)
	default:
		return nil, apperror.BadRequest("unsupported file type: " + ext + " (expected .csv or .xlsx)")
	}
}

func parseCSVImport(r io.Reader) ([]models.ImportRow, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1 // tolerate ragged rows; resolver flags empties

	records, err := cr.ReadAll()
	if err != nil {
		return nil, apperror.BadRequest("malformed CSV: " + err.Error())
	}
	return parseRecords(records)
}

func parseXLSXImport(r io.Reader) ([]models.ImportRow, error) {
	// excelize needs an io.ReadSeeker; buffer the input.
	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, apperror.BadRequest("read xlsx: " + err.Error())
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf))
	if err != nil {
		return nil, apperror.BadRequest("malformed XLSX: " + err.Error())
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, apperror.BadRequest("xlsx has no sheets")
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, apperror.BadRequest("read xlsx rows: " + err.Error())
	}
	return parseRecords(rows)
}

// parseRecords is the shared header-and-rows parser used by both formats.
func parseRecords(records [][]string) ([]models.ImportRow, error) {
	if len(records) == 0 {
		return nil, apperror.BadRequest("file is empty")
	}
	header := records[0]
	if len(header) == 0 {
		return nil, apperror.BadRequest("header row is empty")
	}
	header[0] = stripBOM(header[0])

	// Build name → column-index map (case-insensitive). Detect spec:* columns
	// at the same time.
	type specCol struct {
		key string
		col int
	}
	idx := make(map[string]int, len(header))
	var specs []specCol
	for i, h := range header {
		name := strings.TrimSpace(h)
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "spec:") {
			specs = append(specs, specCol{key: strings.TrimSpace(name[len("spec:"):]), col: i})
			continue
		}
		// Normalize known fixed columns by case-insensitive match against the
		// canonical name; unknown columns are stored under their lowered name
		// so a typo doesn't silently bind to a different field.
		canonical := lower
		for _, c := range fixedImportColumns {
			if strings.EqualFold(c, name) {
				canonical = c
				break
			}
		}
		idx[canonical] = i
	}

	var missing []string
	for _, h := range requiredImportHeaders {
		if _, ok := idx[h]; !ok {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		return nil, apperror.BadRequest("missing required header(s): " + strings.Join(missing, ", "))
	}

	cell := func(row []string, col int) string {
		if col < 0 || col >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[col])
	}
	get := func(row []string, name string) string {
		c, ok := idx[name]
		if !ok {
			return ""
		}
		return cell(row, c)
	}

	out := make([]models.ImportRow, 0, len(records)-1)
	for i := 1; i < len(records); i++ {
		raw := records[i]
		if isEmptyRow(raw) {
			continue // skip blank lines (common in spreadsheets)
		}
		row := models.ImportRow{
			RowNum:                 i + 1, // 1-based: header=1, first data row=2
			PONumber:               get(raw, "poNumber"),
			SupplierName:           get(raw, "supplierName"),
			SupplierContact:        get(raw, "supplierContact"),
			OrderDate:              get(raw, "orderDate"),
			PONotes:                get(raw, "poNotes"),
			POResponsibleUserEmail: get(raw, "poResponsibleUserEmail"),
			LineItemName:           get(raw, "lineItemName"),
			CategorySlug:           get(raw, "categorySlug"),
			Quantity:               get(raw, "quantity"),
			HomeVenueCode:          get(raw, "homeVenueCode"),
			DepartmentCode:         get(raw, "departmentCode"),
			AssetTag:               get(raw, "assetTag"),
			Status:                 get(raw, "status"),
			Condition:              get(raw, "condition"),
			CurrentVenueCode:       get(raw, "currentVenueCode"),
			ResponsibleUserEmail:   get(raw, "responsibleUserEmail"),
			SerialNumber:           get(raw, "serialNumber"),
			PurchaseDate:           get(raw, "purchaseDate"),
			ExpectedReturnDate:     get(raw, "expectedReturnDate"),
			Notes:                  get(raw, "notes"),
		}
		if row.Quantity == "" {
			row.Quantity = "1"
		}
		if len(specs) > 0 {
			sf := make(map[string]string, len(specs))
			for _, s := range specs {
				if v := cell(raw, s.col); v != "" {
					sf[s.key] = v
				}
			}
			if len(sf) > 0 {
				row.SpecFields = sf
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func stripBOM(s string) string {
	const bom = "\ufeff"
	return strings.TrimPrefix(s, bom)
}

func isEmptyRow(r []string) bool {
	for _, c := range r {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// MaxImportFileBytes is the hard upload cap the handler enforces. Larger
// uploads are rejected before parsing — excelize buffers the whole file in
// memory and the validate-resolve loop is O(N) over rows, so capping at 10MB
// keeps a single import bounded.
const MaxImportFileBytes = int64(10 << 20)

// errEmptyFile is returned by ParseImportFile when the body has zero bytes;
// kept exported via the BadRequest message for handler/clients to inspect.
var errEmptyFile = errors.New("file is empty")

// ParseFromMultipart is a convenience for handlers: it reads up to
// MaxImportFileBytes (rejecting larger), then delegates.
func ParseFromMultipart(filename string, r io.Reader, sizeHint int64) ([]models.ImportRow, error) {
	if sizeHint > MaxImportFileBytes {
		return nil, apperror.BadRequest(fmt.Sprintf("file too large: %d bytes (max %d)", sizeHint, MaxImportFileBytes))
	}
	limited := io.LimitReader(r, MaxImportFileBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, apperror.BadRequest("read file: " + err.Error())
	}
	if int64(len(buf)) > MaxImportFileBytes {
		return nil, apperror.BadRequest(fmt.Sprintf("file too large (max %d bytes)", MaxImportFileBytes))
	}
	return ParseImportFile(filename, bytes.NewReader(buf))
}
