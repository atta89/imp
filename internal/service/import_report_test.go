package service

import (
	"bytes"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
)

func TestRenderTemplate_RoundTripsThroughParser(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderTemplate(&buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	rows, err := ParseImportFile("template.csv", &buf)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 example row, got %d", len(rows))
	}
	row := rows[0]
	// Every required field on the example row should be non-empty so a user
	// who downloads the template + edits a copy doesn't trip the parser's
	// required-header check.
	for field, value := range map[string]string{
		"poNumber":               row.PONumber,
		"supplierName":           row.SupplierName,
		"orderDate":              row.OrderDate,
		"poResponsibleUserEmail": row.POResponsibleUserEmail,
		"lineItemName":           row.LineItemName,
		"categorySlug":           row.CategorySlug,
		"homeVenueCode":          row.HomeVenueCode,
	} {
		if value == "" {
			t.Errorf("template example row missing %s", field)
		}
	}
}

func TestRenderResult_GroupsByPOWithErrorsBlock(t *testing.T) {
	poID := bson.NewObjectID()
	assetID := bson.NewObjectID()
	created := []CreatedPO{{
		PONumber: "PO-1",
		POID:     poID,
		Assets: []CreatedAsset{{
			AssetTag:         "LAP-0001",
			AssetID:          assetID,
			CategorySlug:     "laptop",
			HomeVenueCode:    "HQ",
			CurrentVenueCode: "HQ",
			ResponsibleEmail: "pat@example.com",
			Status:           "available",
			Condition:        "new",
		}},
	}}
	field := "poNumber"
	errs := []models.ImportRowError{{Row: 42, Field: &field, Message: "PO conflict"}}

	var buf bytes.Buffer
	if err := RenderResult(&buf, created, errs); err != nil {
		t.Fatalf("render result: %v", err)
	}
	out := buf.String()

	// Header + at least one asset row + an errors marker line.
	if !strings.Contains(out, "poNumber,poId,assetTag") {
		t.Errorf("missing main header in:\n%s", out)
	}
	if !strings.Contains(out, "PO-1") || !strings.Contains(out, "LAP-0001") {
		t.Errorf("missing PO/asset row in:\n%s", out)
	}
	if !strings.Contains(out, "# Errors") {
		t.Errorf("missing errors marker in:\n%s", out)
	}
	if !strings.Contains(out, "PO conflict") {
		t.Errorf("missing error message in:\n%s", out)
	}
}

func TestRenderResult_NoErrorsBlockWhenNone(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderResult(&buf, nil, nil); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(buf.String(), "# Errors") {
		t.Errorf("should not emit errors block when none; got:\n%s", buf.String())
	}
}
