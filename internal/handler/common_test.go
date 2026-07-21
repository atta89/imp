package handler

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gofiber/fiber/v2"

	"imp/internal/middleware"
	"imp/internal/pagination"
)

// captureKeyset mounts a route that runs ParseKeyset and reports the result
// via response headers, so we can assert parsing without a service.
func captureKeyset(t *testing.T, url string) (status, limit int, hasCursor bool) {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))})
	app.Get("/x", func(c *fiber.Ctx) error {
		cur, lim, err := ParseKeyset(c)
		if err != nil {
			return err
		}
		c.Set("X-Limit", strconv.Itoa(lim))
		c.Set("X-Has-Cursor", boolStr(cur != nil))
		return c.SendStatus(fiber.StatusOK)
	})
	resp, err := app.Test(httptest.NewRequest("GET", url, nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	l, _ := strconv.Atoi(resp.Header.Get("X-Limit"))
	return resp.StatusCode, l, resp.Header.Get("X-Has-Cursor") == "true"
}

func TestParseKeysetDefaults(t *testing.T) {
	status, limit, hasCursor := captureKeyset(t, "/x")
	if status != 200 || limit != defaultLimit || hasCursor {
		t.Fatalf("status=%d limit=%d hasCursor=%v", status, limit, hasCursor)
	}
}

func TestParseKeysetClampsLimit(t *testing.T) {
	_, limit, _ := captureKeyset(t, "/x?limit=9999")
	if limit != maxLimit {
		t.Fatalf("limit=%d, want %d", limit, maxLimit)
	}
}

func TestParseKeysetValidCursor(t *testing.T) {
	tok := pagination.Encode(pagination.Cursor{})
	_, _, hasCursor := captureKeyset(t, "/x?cursor="+tok)
	if !hasCursor {
		t.Fatal("expected cursor to be parsed")
	}
}

func TestParseKeysetMalformedCursorIs400(t *testing.T) {
	status, _, _ := captureKeyset(t, "/x?cursor=@@bad@@")
	if status != 400 {
		t.Fatalf("status=%d, want 400", status)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
