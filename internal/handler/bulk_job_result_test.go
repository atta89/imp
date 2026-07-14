package handler

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"imp/internal/middleware"
)

// TestSendArtifact_StreamsFullBody guards the Fiber pitfall that caused a 500 on
// GET /assets/bulk/jobs/:id/result for a completed job: c.SendStream reads the
// response body AFTER the handler returns, so a reader closed on return (as an
// *os.File from FileStorage.Get requires) was read after close and the stream
// errored. sendArtifact must buffer the reader fully before returning. The
// strictReadCloser (see attachment_test.go) errors on read-after-close, so a
// regression to the SendStream+defer-close pattern cannot slip through.
func TestSendArtifact_StreamsFullBody(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler(discard)})

	payload := []byte(`{"jobId":"6a55dc0e42071683f23fcd7e","count":2,"truncated":false,"assetIds":["6a4c8189a450f355fd047bc4","6a4c8189a450f355fd047bc5"]}`)
	app.Get("/result", func(c *fiber.Ctx) error {
		return sendArtifact(c, &strictReadCloser{data: payload}, "application/json", "asset-ids.json")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/result", nil), -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, got)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("body = %q, want %q", got, payload)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="asset-ids.json"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
}
