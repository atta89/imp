package service

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

const happyCSV = "poNumber,supplierName,orderDate,poResponsibleUserEmail,lineItemName,categorySlug,quantity,homeVenueCode,assetTag,status,condition,spec:cpu\n" +
	"PO-001,Acme Corp,2026-01-15,pat@example.com,MacBook Pro,laptop,1,HQ,LAP-9001,available,new,M3 Pro\n" +
	"PO-001,Acme Corp,2026-01-15,pat@example.com,MacBook Pro,laptop,1,HQ,LAP-9002,in_use,good,M3 Max\n" +
	"PO-002,Globex,2026-02-01,sam@example.com,Office Chair,chair,10,WH,,,,\n"

func TestParseImportFile_CSV_HappyPath(t *testing.T) {
	rows, err := ParseImportFile("import.csv", strings.NewReader(happyCSV))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[0].RowNum != 2 || rows[0].PONumber != "PO-001" || rows[0].AssetTag != "LAP-9001" {
		t.Errorf("row 0 unexpected: %+v", rows[0])
	}
	if rows[0].SpecFields == nil || rows[0].SpecFields["cpu"] != "M3 Pro" {
		t.Errorf("expected spec:cpu populated, got %+v", rows[0].SpecFields)
	}
	if rows[2].Quantity != "10" {
		t.Errorf("row 2 quantity: want 10, got %q", rows[2].Quantity)
	}
	if rows[2].AssetTag != "" {
		t.Errorf("row 2 assetTag should be empty, got %q", rows[2].AssetTag)
	}
	if rows[2].SpecFields != nil {
		t.Errorf("row 2 should have nil SpecFields when all spec cells blank, got %+v", rows[2].SpecFields)
	}
}

func TestParseImportFile_CSV_MissingHeader_PONumber(t *testing.T) {
	bad := "supplierName,orderDate,poResponsibleUserEmail,lineItemName,categorySlug,homeVenueCode\nAcme,2026-01-15,pat@example.com,Chair,chair,HQ\n"
	_, err := ParseImportFile("x.csv", strings.NewReader(bad))
	if err == nil {
		t.Fatal("expected error for missing poNumber header")
	}
	if !strings.Contains(err.Error(), "poNumber") {
		t.Errorf("error should mention poNumber, got: %v", err)
	}
}

func TestParseImportFile_CSV_BOMTolerated(t *testing.T) {
	bommed := "\ufeff" + happyCSV
	rows, err := ParseImportFile("x.csv", strings.NewReader(bommed))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rows[0].PONumber != "PO-001" {
		t.Errorf("BOM should be stripped from first header; got PONumber %q", rows[0].PONumber)
	}
}

func TestParseImportFile_CSV_QuantityDefaultsToOne_WhenColumnAbsent(t *testing.T) {
	csv := "poNumber,supplierName,orderDate,poResponsibleUserEmail,lineItemName,categorySlug,homeVenueCode\nPO-1,Acme,2026-01-15,a@b.com,Chair,chair,HQ\n"
	rows, err := ParseImportFile("x.csv", strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rows[0].Quantity != "1" {
		t.Errorf("expected default quantity 1, got %q", rows[0].Quantity)
	}
}

func TestParseImportFile_CSV_SkipsBlankRows(t *testing.T) {
	csv := happyCSV + "\n   ,,,,,,,,,,,\n"
	rows, err := ParseImportFile("x.csv", strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("expected 3 non-blank rows, got %d", len(rows))
	}
}

func TestParseImportFile_CSV_CaseInsensitiveHeaders(t *testing.T) {
	csv := "PONUMBER,SupplierName,ORDERDATE,poResponsibleUserEmail,lineItemName,CategorySlug,homevenuecode\nPO-1,Acme,2026-01-15,a@b.com,Chair,chair,HQ\n"
	rows, err := ParseImportFile("x.csv", strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rows[0].PONumber != "PO-1" || rows[0].HomeVenueCode != "HQ" {
		t.Errorf("case-insensitive header match failed: %+v", rows[0])
	}
}

func TestParseImportFile_UnsupportedExtension(t *testing.T) {
	_, err := ParseImportFile("x.txt", strings.NewReader("anything"))
	if err == nil {
		t.Fatal("expected error for .txt")
	}
}

// buildXLSX writes the same shape as happyCSV but as a real .xlsx file via
// excelize, then returns the bytes for ParseImportFile to consume.
func buildXLSX(t *testing.T) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	sheet := f.GetSheetName(0)
	headers := []string{"poNumber", "supplierName", "orderDate", "poResponsibleUserEmail", "lineItemName", "categorySlug", "quantity", "homeVenueCode", "assetTag", "status", "condition", "spec:cpu"}
	for c, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(c+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
	}
	rowsData := [][]any{
		{"PO-001", "Acme Corp", "2026-01-15", "pat@example.com", "MacBook Pro", "laptop", 1, "HQ", "LAP-9001", "available", "new", "M3 Pro"},
		{"PO-001", "Acme Corp", "2026-01-15", "pat@example.com", "MacBook Pro", "laptop", 1, "HQ", "LAP-9002", "in_use", "good", "M3 Max"},
		{"PO-002", "Globex", "2026-02-01", "sam@example.com", "Office Chair", "chair", 10, "WH", "", "", "", ""},
	}
	for r, row := range rowsData {
		for c, v := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, r+2)
			_ = f.SetCellValue(sheet, cell, v)
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	return buf.Bytes()
}

func TestParseImportFile_XLSX_HappyPath(t *testing.T) {
	data := buildXLSX(t)
	rows, err := ParseImportFile("import.xlsx", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parse xlsx: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[0].PONumber != "PO-001" || rows[0].AssetTag != "LAP-9001" {
		t.Errorf("xlsx row 0: %+v", rows[0])
	}
	if rows[0].SpecFields == nil || rows[0].SpecFields["cpu"] != "M3 Pro" {
		t.Errorf("xlsx spec col missing: %+v", rows[0].SpecFields)
	}
}

func TestParseFromMultipart_RejectsOversize(t *testing.T) {
	// Hint exceeds cap → rejected before reading.
	_, err := ParseFromMultipart("x.csv", strings.NewReader("a,b\n1,2\n"), MaxImportFileBytes+1)
	if err == nil {
		t.Fatal("expected oversize rejection")
	}
}
