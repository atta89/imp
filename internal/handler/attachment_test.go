package handler

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/middleware"
	"imp/internal/models"
)

// Note: full end-to-end tests live in integration tests. This handler test
// only verifies the request-parsing/wiring layer; the service is stubbed.
func TestAttachmentHandler_Upload_MissingFile(t *testing.T) {
	// Wire the same error handler production uses so apperror.* return
	// values map to their real HTTP status instead of Fiber's generic 500.
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler(discardLogger)})
	h := &AttachmentHandler{}
	app.Post("/attachments", func(c *fiber.Ctx) error {
		c.Locals(middleware.LocalUserID, bson.NewObjectID())
		return h.Upload(c)
	})

	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	// Deliberately write no "file" part.
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/attachments", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		got, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, body = %s", resp.StatusCode, got)
	}
}

// stubDownloader is a narrow stub for the attachmentDownloader interface the
// Download handler depends on. Lets us unit-test RBAC/status-code wiring
// without a live Mongo.
type stubDownloader struct {
	att      *models.Attachment
	assets   []models.Asset
	getBytes func() io.ReadCloser
}

func (s *stubDownloader) Upload(ctx context.Context, uploadedBy bson.ObjectID, filename string, r io.Reader) (*models.Attachment, error) {
	return s.att, nil
}

func (s *stubDownloader) Fetch(ctx context.Context, id bson.ObjectID) (*models.Attachment, error) {
	return s.att, nil
}

func (s *stubDownloader) LoadForRBAC(ctx context.Context, att *models.Attachment) ([]models.Asset, error) {
	return s.assets, nil
}

func (s *stubDownloader) GetBytes(ctx context.Context, key string) (io.ReadCloser, error) {
	if s.getBytes != nil {
		return s.getBytes(), nil
	}
	return io.NopCloser(bytes.NewReader([]byte("bytes"))), nil
}

// strictReadCloser mimics *os.File: Read errors after Close, and it does not
// implement WriteTo, so fasthttp's fast paths cannot bypass Read. Used to
// catch premature-close bugs that plain bytes.Reader + io.NopCloser would hide.
type strictReadCloser struct {
	data   []byte
	pos    int
	closed bool
}

func (s *strictReadCloser) Read(p []byte) (int, error) {
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += n
	return n, nil
}

func (s *strictReadCloser) Close() error { s.closed = true; return nil }

// TestAttachmentHandler_Download_HappyPath_StreamsFullBody covers the bug
// where the handler deferred Close on the storage reader before Fiber had
// written the response body. Because the stream is set on the response and
// read AFTER the handler returns, the defer closed the reader too early and
// the body arrived empty (or as a stream error). This test uses a Reader
// that errors on read-after-close so the regression cannot slip through.
func TestAttachmentHandler_Download_HappyPath_StreamsFullBody(t *testing.T) {
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler(discardLogger)})

	payload := []byte("hello, this is the real file body")
	attID := bson.NewObjectID()
	key := "somestoragekey"
	stub := &stubDownloader{
		att: &models.Attachment{
			ID:          attID,
			Linked:      true,
			StorageKey:  &key,
			ContentType: "image/png",
			Filename:    "photo.png",
			Size:        int64(len(payload)),
		},
		getBytes: func() io.ReadCloser {
			return &strictReadCloser{data: payload}
		},
	}
	h := &AttachmentHandler{svc: stub}
	app.Get("/attachments/:id/download", func(c *fiber.Ctx) error {
		c.Locals(middleware.LocalUserID, bson.NewObjectID())
		c.Locals(middleware.LocalRole, models.Admin) // admin bypasses per-asset RBAC
		return h.Download(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/attachments/"+attID.Hex()+"/download", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
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
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="photo.png"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
}

// TestAttachmentHandler_Download_UnlinkedReturns404 verifies unlinked
// attachments 404 for every caller, including non-admins — this is the
// invariant that keeps un-vetted uploads from being fetchable before they're
// attached to an asset.
func TestAttachmentHandler_Download_UnlinkedReturns404(t *testing.T) {
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler(discardLogger)})

	attID := bson.NewObjectID()
	stub := &stubDownloader{
		att: &models.Attachment{
			ID:     attID,
			Linked: false,
		},
	}
	h := &AttachmentHandler{svc: stub}
	app.Get("/attachments/:id/download", func(c *fiber.Ctx) error {
		c.Locals(middleware.LocalUserID, bson.NewObjectID())
		c.Locals(middleware.LocalRole, models.Staff)
		return h.Download(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/attachments/"+attID.Hex()+"/download", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		got, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, body = %s", resp.StatusCode, got)
	}
}

// TestAttachmentHandler_Download_UnlinkedReturns404_EvenForAdmin verifies the
// load-bearing invariant: unlinked attachments return 404 EVEN for admins.
// The linked-check runs before the admin bypass, so an admin trying to
// download an unlinked attachment also gets 404 — this ensures un-vetted
// uploads are never fetchable until attached to an asset.
func TestAttachmentHandler_Download_UnlinkedReturns404_EvenForAdmin(t *testing.T) {
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler(discardLogger)})

	attID := bson.NewObjectID()
	stub := &stubDownloader{
		att: &models.Attachment{
			ID:     attID,
			Linked: false,
		},
	}
	h := &AttachmentHandler{svc: stub}
	app.Get("/attachments/:id/download", func(c *fiber.Ctx) error {
		c.Locals(middleware.LocalUserID, bson.NewObjectID())
		c.Locals(middleware.LocalRole, models.Admin)
		return h.Download(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/attachments/"+attID.Hex()+"/download", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		got, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, body = %s", resp.StatusCode, got)
	}
}
