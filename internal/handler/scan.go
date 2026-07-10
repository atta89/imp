package handler

import (
	"context"

	"github.com/gofiber/fiber/v2"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/service"
	"imp/pkg/response"
)

// scanViewer is the narrow slice of AssetService the scan handler depends on.
// Kept as an interface (rather than the concrete service type) so unit tests
// can stub it without a live Mongo. *service.AssetService satisfies this.
type scanViewer interface {
	ScanView(ctx context.Context, p service.Principal, qrToken string) (*models.ScanAssetView, error)
}

// ScanHandler serves GET /scan/:qrToken — the authenticated QR-scan lookup.
// The route lives in the JWT-protected group, and the service enforces per-asset
// RBAC (admin, venue scope, or current custodian), so this is no longer a
// public endpoint.
type ScanHandler struct {
	svc scanViewer
}

func NewScanHandler(svc *service.AssetService) *ScanHandler {
	return &ScanHandler{svc: svc}
}

// Scan resolves a scanned QR token to an asset view for the authenticated,
// authorized caller. Missing/invalid JWT is rejected upstream by RequireAuth
// (401); the service returns 403 when the caller is out of scope and 404 for an
// unknown token.
func (h *ScanHandler) Scan(c *fiber.Ctx) error {
	token := c.Params("qrToken")
	if token == "" {
		return apperror.BadRequest("qrToken is required")
	}
	view, err := h.svc.ScanView(c.Context(), principal(c), token)
	if err != nil {
		return err
	}
	return response.OK(c, view)
}
