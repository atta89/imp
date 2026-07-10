package handler

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// TestImportHandler_Template_StreamsCSV verifies the template endpoint
// returns a CSV body with the expected headers. The handler doesn't touch
// Mongo for this path so we can test it without any fixtures.
func TestImportHandler_Template_StreamsCSV(t *testing.T) {
	h := &ImportHandler{}
	app := fiber.New()
	app.Get("/imports/purchase-orders/template", h.Template)

	req := httptest.NewRequest("GET", "/imports/purchase-orders/template", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status: want 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content-type: want text/csv*, got %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "poNumber") || !strings.Contains(string(body), "categorySlug") {
		t.Errorf("template body missing expected headers:\n%s", string(body))
	}
}
