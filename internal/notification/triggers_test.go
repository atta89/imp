package notification

import (
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
)

func mkUser(active, notify bool) *models.User {
	return &models.User{
		ID:            bson.NewObjectID(),
		Name:          "Jane",
		Email:         openapi_types.Email("jane@example.com"),
		IsActive:      active,
		NotifyByEmail: notify,
	}
}

func TestBuildBulkCustodyAssignedNotification_HappyPath(t *testing.T) {
	u := mkUser(true, true)
	refs := []BulkCustodyAssignedRef{
		{AssetID: bson.NewObjectID(), Tag: "IT-001", Name: "Laptop A", VenueName: "HQ", QRToken: "tok-a"},
		{AssetID: bson.NewObjectID(), Tag: "IT-002", Name: "Laptop B", VenueName: "Branch", QRToken: "tok-b"},
	}
	n := buildBulkCustodyAssignedNotification(u, refs, func(tok string) string { return "https://example.com/scan/" + tok })
	if n == nil {
		t.Fatal("expected a notification; got nil")
	}
	if n.Type != models.CustodyChange {
		t.Errorf("Type: want custody_change; got %s", n.Type)
	}
	if n.RecipientUserID != u.ID {
		t.Errorf("RecipientUserID: want %s; got %s", u.ID.Hex(), n.RecipientUserID.Hex())
	}
	if n.RecipientEmail != u.Email {
		t.Errorf("RecipientEmail: want %s; got %s", u.Email, n.RecipientEmail)
	}
	if n.Subject == "" {
		t.Error("Subject should be non-empty")
	}
	if n.Body == "" {
		t.Error("Body should be non-empty")
	}
	// Body must list each asset (tag, name, venue).
	for _, r := range refs {
		if !containsAll(n.Body, r.Tag, r.Name, r.VenueName) {
			t.Errorf("Body missing tag/name/venue for %s", r.Tag)
		}
	}
}

func TestBuildBulkCustodyAssignedNotification_ReturnsNilWhenNotifyDisabled(t *testing.T) {
	u := mkUser(true, false) // NotifyByEmail=false
	refs := []BulkCustodyAssignedRef{{AssetID: bson.NewObjectID(), Tag: "T", Name: "N", VenueName: "V", QRToken: "x"}}
	if n := buildBulkCustodyAssignedNotification(u, refs, func(s string) string { return s }); n != nil {
		t.Error("expected nil when NotifyByEmail=false")
	}
}

func TestBuildBulkCustodyAssignedNotification_ReturnsNilWhenInactive(t *testing.T) {
	u := mkUser(false, true) // IsActive=false
	refs := []BulkCustodyAssignedRef{{AssetID: bson.NewObjectID(), Tag: "T", Name: "N", VenueName: "V", QRToken: "x"}}
	if n := buildBulkCustodyAssignedNotification(u, refs, func(s string) string { return s }); n != nil {
		t.Error("expected nil when user is inactive")
	}
}

func TestBuildBulkCustodyAssignedNotification_ReturnsNilForEmptyRefs(t *testing.T) {
	u := mkUser(true, true)
	if n := buildBulkCustodyAssignedNotification(u, nil, func(s string) string { return s }); n != nil {
		t.Error("expected nil when refs is empty (nothing to digest)")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(sub) == 0 || indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	n, m := len(s), len(sub)
	if m == 0 {
		return 0
	}
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}
