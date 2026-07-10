package handler

import (
	"context"
	"io"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/middleware"
	"imp/internal/models"
	"imp/internal/service"
	"imp/pkg/response"
)

// attachmentDownloader is the narrow slice of AttachmentService the handler
// depends on. Kept as an interface (rather than the concrete service type)
// so unit tests can stub it without a live Mongo/storage backend.
// *service.AttachmentService satisfies this.
type attachmentDownloader interface {
	Upload(ctx context.Context, uploadedBy bson.ObjectID, filename string, r io.Reader) (*models.Attachment, error)
	Fetch(ctx context.Context, id bson.ObjectID) (*models.Attachment, error)
	LoadForRBAC(ctx context.Context, att *models.Attachment) ([]models.Asset, error)
	GetBytes(ctx context.Context, key string) (io.ReadCloser, error)
}

type AttachmentHandler struct {
	svc attachmentDownloader
}

func NewAttachmentHandler(svc *service.AttachmentService) *AttachmentHandler {
	return &AttachmentHandler{svc: svc}
}

// Upload handles POST /attachments — multipart with a single "file" part.
func (h *AttachmentHandler) Upload(c *fiber.Ctx) error {
	userID := middleware.CurrentUserID(c)
	if userID.IsZero() {
		return apperror.Unauthorized("missing user")
	}
	fh, err := c.FormFile("file")
	if err != nil {
		return apperror.BadRequest("missing 'file' part")
	}
	f, err := fh.Open()
	if err != nil {
		return apperror.BadRequest("cannot open upload")
	}
	defer f.Close()

	att, err := h.svc.Upload(c.Context(), userID, fh.Filename, f)
	if err != nil {
		return err
	}
	return response.OK(c, models.AttachmentUploadResponse{
		AttachmentId: att.ID,
		Filename:     att.Filename,
		ContentType:  att.ContentType,
		Size:         att.Size,
	})
}

// Download streams an attachment's bytes. RBAC: admins always pass; other
// callers must have venue access (home or current) or be the responsible
// custodian on at least one linked asset. Unlinked attachments always 404 —
// including for admins — since an unlinked doc hasn't been vetted onto any
// asset yet.
func (h *AttachmentHandler) Download(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := bson.ObjectIDFromHex(idStr)
	if err != nil {
		return apperror.BadRequest("invalid attachment id")
	}
	userID := middleware.CurrentUserID(c)
	if userID.IsZero() {
		return apperror.Unauthorized("missing user")
	}

	att, err := h.svc.Fetch(c.Context(), id)
	if err != nil {
		return err
	}
	// Unlinked → 404 for everyone, including admin.
	if !att.Linked {
		return apperror.NotFound("attachment not found")
	}

	if !middleware.IsAdmin(c) {
		assets, err := h.svc.LoadForRBAC(c.Context(), att)
		if err != nil {
			return err
		}
		if !canAccessAnyAsset(c, assets) {
			return apperror.Forbidden("no access to attachment")
		}
	}

	if att.StorageKey == nil {
		return apperror.Internal("attachment has no storage key", nil)
	}
	rc, err := h.svc.GetBytes(c.Context(), *att.StorageKey)
	if err != nil {
		return apperror.Internal("open attachment bytes", err)
	}
	defer rc.Close()

	// Buffer the whole file before writing the response. c.SendStream in Fiber
	// registers the reader for later — the actual read happens after the
	// handler returns, so a deferred rc.Close() would close it before fasthttp
	// could read it. Attachments are capped at 10 MB (ATTACHMENT_MAX_BYTES),
	// so buffering is bounded.
	body, err := io.ReadAll(rc)
	if err != nil {
		return apperror.Internal("read attachment bytes", err)
	}

	c.Set(fiber.HeaderContentType, att.ContentType)
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+safeFilename(att.Filename)+`"`)
	return c.Send(body)
}

// canAccessAnyAsset returns true if the caller can access at least one of the
// given assets. It reuses the shared service.Principal.CanAccessAsset rule
// (venue scope on home or current, or current custodian) so this download RBAC
// and the authenticated scan RBAC cannot drift. Returns on the first match.
func canAccessAnyAsset(c *fiber.Ctx, assets []models.Asset) bool {
	p := principal(c)
	for i := range assets {
		if p.CanAccessAsset(&assets[i]) {
			return true
		}
	}
	return false
}

// safeFilename strips characters that would break the Content-Disposition
// header (quotes, backslashes, control chars). Best-effort — not a strict
// RFC 5987 implementation — matching the plan's stated MVP scope.
// Uses utf8.AppendRune to preserve multi-byte UTF-8 sequences correctly.
func safeFilename(name string) string {
	buf := make([]byte, 0, len(name))
	for _, r := range name {
		if r == '"' || r == '\\' || r < 0x20 {
			buf = append(buf, '_')
			continue
		}
		buf = utf8.AppendRune(buf, r)
	}
	return string(buf)
}
