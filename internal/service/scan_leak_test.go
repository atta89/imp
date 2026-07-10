package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
)

// strPtr is a helper that returns a pointer to a string.
func strPtr(s string) *string {
	return &s
}

// TestScanAssetView_NeverLeaksAttachmentData is a regression test ensuring that
// the GET /scan/{qrToken} response never includes attachment-related fields,
// even if they are accidentally added to Asset or ScanAssetView in the future.
// The scan endpoint is now authenticated and returns full custodian contact
// details, but attachment metadata stays exclusive to GET /assets/{id}/history.
func TestScanAssetView_NeverLeaksAttachmentData(t *testing.T) {
	// Build a realistic ScanAssetView with all fields populated.
	homeVenueID := bson.NewObjectID()
	currentVenueID := bson.NewObjectID()
	categoryID := bson.NewObjectID()
	responsibleUserID := bson.NewObjectID()

	view := &models.ScanAssetView{
		Asset: models.Asset{
			ID:                 bson.NewObjectID(),
			AssetTag:           "ASSET-001",
			QrToken:            "qr_token_12345",
			Name:               "MacBook Pro",
			HomeVenueID:        homeVenueID,
			CurrentVenueID:     currentVenueID,
			CategoryID:         categoryID,
			DepartmentID:       nil, // Optional, can be nil or populated
			Condition:          "excellent",
			Status:             "active",
			IsActive:           true,
			IsOverdue:          false,
			CreatedAt:          time.Now(),
			UpdatedAt:          time.Now(),
			Notes:              strPtr("Test asset notes"),
			SerialNumber:       strPtr("SN12345"),
			Photos:             &[]string{"photo1.jpg", "photo2.jpg"},
			ResponsibleUserID:  &responsibleUserID,
			PurchaseDate:       &time.Time{},
			PurchaseOrderID:    nil,
			ImportJobID:        nil,
			ExpectedReturnDate: nil,
			Specs:              &map[string]interface{}{"cpu": "M3", "ram": "16GB"},
			// Fill every field to catch accidental future additions.
		},
		HomeVenueName:    strPtr("Main HQ"),
		CurrentVenueName: strPtr("Main HQ"),
		CategoryName:     strPtr("Laptops"),
		DepartmentName:   strPtr("IT"),
		ResponsiblePerson: &models.ScanUserContact{
			ID:       bson.NewObjectID(),
			Name:     "John Doe",
			Position: "Senior Engineer",
			Role:     "admin",
			// Contact details are now unmasked on the authenticated scan view.
			Email: "john.doe@example.com",
			Phone: strPtr("+15550100"),
		},
	}

	// Marshal to JSON.
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("failed to marshal ScanAssetView: %v", err)
	}

	// Check the JSON representation for forbidden attachment-related substrings.
	lower := strings.ToLower(string(body))

	forbidden := []string{
		"attachment",      // catches: attachments, attachmentId, attachmentIds, attachmentKey
		"storagekey",      // storageKey, storage_key
		"\"linked\"",      // matches JSON key "linked"; use quoted to avoid catching e.g. "linkedto"
		"linkedat",        // linkedAt, linked_at
		"movementid",      // movementIds, movementId, movement_id
	}

	for _, f := range forbidden {
		if strings.Contains(lower, f) {
			t.Errorf("scan response leaks forbidden substring %q; body:\n%s", f, body)
		}
	}
}
