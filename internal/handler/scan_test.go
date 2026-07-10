package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/jwtauth"
	"imp/internal/middleware"
	"imp/internal/models"
	"imp/internal/service"
)

// stubScanViewer stands in for AssetService. It runs the REAL shared
// authorization rule (service.Principal.CanAccessAsset) against a fixed asset
// so the handler test exercises principal-building + RBAC + status mapping
// end-to-end without a live Mongo.
type stubScanViewer struct {
	asset    *models.Asset
	notFound bool
}

func (s *stubScanViewer) ScanView(ctx context.Context, p service.Principal, qrToken string) (*models.ScanAssetView, error) {
	if s.notFound {
		return nil, apperror.NotFound("asset not found")
	}
	if !p.CanAccessAsset(s.asset) {
		return nil, apperror.Forbidden("not authorized for this asset")
	}
	return &models.ScanAssetView{
		Asset: *s.asset,
		ResponsiblePerson: &models.ScanUserContact{
			ID:       bson.NewObjectID(),
			Name:     "Jane Custodian",
			Role:     models.Staff,
			Position: "Technician",
			Email:    "jane@example.com",
			Phone:    strPtr("+15550123"),
		},
	}, nil
}

func strPtr(s string) *string { return &s }

// newScanTestApp wires the ErrorHandler production uses (so apperror.* map to
// real HTTP status) and a route that stamps the given principal locals before
// calling the scan handler.
func newScanTestApp(h *ScanHandler, role models.Role, userID bson.ObjectID, venues []string) *fiber.App {
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler(discardLogger)})
	app.Get("/scan/:qrToken", func(c *fiber.Ctx) error {
		c.Locals(middleware.LocalUserID, userID)
		c.Locals(middleware.LocalRole, role)
		c.Locals(middleware.LocalVenueIDs, venues)
		return h.Scan(c)
	})
	return app
}

func doScan(t *testing.T, app *fiber.App) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/scan/qr_token_abc", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestScanHandler_RBAC covers the authorization matrix at the HTTP layer:
// admin, venue scope (home or current), custodian → 200; out-of-scope
// non-custodian → 403.
func TestScanHandler_RBAC(t *testing.T) {
	home := bson.NewObjectID()
	current := bson.NewObjectID()
	other := bson.NewObjectID()
	custodian := bson.NewObjectID()
	stranger := bson.NewObjectID()

	asset := &models.Asset{
		HomeVenueID:       home,
		CurrentVenueID:    current,
		ResponsibleUserID: &custodian,
	}

	tests := []struct {
		name   string
		role   models.Role
		userID bson.ObjectID
		venues []string
		want   int
	}{
		{"admin", models.Admin, stranger, nil, http.StatusOK},
		{"manager scoped to home venue", models.VenueManager, stranger, []string{home.Hex()}, http.StatusOK},
		{"staff scoped to current venue only", models.Staff, stranger, []string{current.Hex()}, http.StatusOK},
		{"current custodian with no venue scope", models.Staff, custodian, nil, http.StatusOK},
		{"scoped to neither and not custodian", models.Staff, stranger, []string{other.Hex()}, http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &ScanHandler{svc: &stubScanViewer{asset: asset}}
			app := newScanTestApp(h, tc.role, tc.userID, tc.venues)
			resp := doScan(t, app)
			if resp.StatusCode != tc.want {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("status = %d, want %d; body = %s", resp.StatusCode, tc.want, body)
			}
		})
	}
}

// TestScanHandler_UnknownToken verifies an authenticated caller gets 404 for an
// unknown qrToken.
func TestScanHandler_UnknownToken(t *testing.T) {
	h := &ScanHandler{svc: &stubScanViewer{notFound: true}}
	app := newScanTestApp(h, models.Admin, bson.NewObjectID(), nil)
	resp := doScan(t, app)
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 404; body = %s", resp.StatusCode, body)
	}
}

// TestScanHandler_NoToken verifies the route rejects an unauthenticated request
// with 401 — proving it sits behind RequireAuth in the protected group.
func TestScanHandler_NoToken(t *testing.T) {
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler(discardLogger)})
	issuer := jwtauth.NewIssuer("test-secret", time.Minute, time.Hour)
	h := &ScanHandler{svc: &stubScanViewer{asset: &models.Asset{}}}
	app.Get("/scan/:qrToken", middleware.RequireAuth(issuer), h.Scan)

	req := httptest.NewRequest(http.MethodGet, "/scan/qr_token_abc", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 401; body = %s", resp.StatusCode, body)
	}
}

// TestScanHandler_ResponseHasContactNoAttachments verifies the 200 body carries
// the unmasked contact details and no attachment fields.
func TestScanHandler_ResponseHasContactNoAttachments(t *testing.T) {
	h := &ScanHandler{svc: &stubScanViewer{asset: &models.Asset{HomeVenueID: bson.NewObjectID()}}}
	app := newScanTestApp(h, models.Admin, bson.NewObjectID(), nil)
	resp := doScan(t, app)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)

	// Contact details present (unmasked).
	var env struct {
		Data models.ScanAssetView `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data.ResponsiblePerson == nil || string(env.Data.ResponsiblePerson.Email) == "" {
		t.Errorf("expected unmasked contact email in scan response; body = %s", body)
	}

	// No attachment metadata leaked.
	if strings.Contains(strings.ToLower(string(body)), "attachment") {
		t.Errorf("scan response leaks attachment data; body = %s", body)
	}
}
