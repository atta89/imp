package handler

import (
	"bytes"
	"encoding/json"

	"github.com/gofiber/fiber/v2"

	"imp/internal/apperror"
	"imp/internal/middleware"
	"imp/internal/models"
	"imp/internal/repository"
	"imp/internal/service"
	"imp/pkg/response"
)

// ImportHandler implements the /imports/purchase-orders/* endpoints (PRD §6.12).
// Admin-only — the router gates these with adminOnly so no per-handler check.
//
// The handler holds the repos that ImportService.Report needs for its
// asset/PO/venue/category/user lookups. Keeps the service constructor narrow
// without forcing every consumer to thread the same set of repos.
type ImportHandler struct {
	svc        *service.ImportService
	assets     *repository.AssetRepository
	pos        *repository.PurchaseOrderRepository
	venues     *repository.VenueRepository
	categories *repository.CategoryRepository
	users      *repository.UserRepository
}

func NewImportHandler(
	svc *service.ImportService,
	assets *repository.AssetRepository,
	pos *repository.PurchaseOrderRepository,
	venues *repository.VenueRepository,
	categories *repository.CategoryRepository,
	users *repository.UserRepository,
) *ImportHandler {
	return &ImportHandler{
		svc:        svc,
		assets:     assets,
		pos:        pos,
		venues:     venues,
		categories: categories,
		users:      users,
	}
}

// Template streams the static CSV template used by admins to start a bulk
// import.
func (h *ImportHandler) Template(c *fiber.Ctx) error {
	var buf bytes.Buffer
	if err := service.RenderTemplate(&buf); err != nil {
		return apperror.Internal("render template", err)
	}
	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="po-import-template.csv"`)
	return c.Status(fiber.StatusOK).Send(buf.Bytes())
}

// Validate parses the uploaded file, resolves+validates every row, persists
// a preview-ready ImportJob, and returns the preview. Writes no POs or
// assets.
func (h *ImportHandler) Validate(c *fiber.Ctx) error {
	fh, err := c.FormFile("file")
	if err != nil {
		return apperror.BadRequest("file is required (multipart field name: file)")
	}
	if fh.Size > service.MaxImportFileBytes {
		return apperror.BadRequest("file too large")
	}
	f, err := fh.Open()
	if err != nil {
		return apperror.BadRequest("read upload: " + err.Error())
	}
	defer f.Close()

	// Optional `options` field carries the import knobs as JSON. Empty is OK
	// — defaults apply.
	var opts models.ImportJobOptions
	if raw := c.FormValue("options"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &opts); err != nil {
			return apperror.BadRequest("invalid options JSON: " + err.Error())
		}
	}

	preview, err := h.svc.Validate(c.Context(), fh.Filename, f, fh.Size, opts, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.OK(c, preview)
}

// Commit applies a previously-validated import. Creates POs + assets, one
// Mongo transaction per PO. Notifications suppressed throughout.
func (h *ImportHandler) Commit(c *fiber.Ctx) error {
	var req models.CommitImportRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	opts := models.ImportJobOptions{}
	if req.Options != nil {
		opts = *req.Options
	}
	job, err := h.svc.Commit(c.Context(), req.ImportJobID, opts, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.OK(c, job)
}

// Get returns the current state + counts + errors of an import job.
func (h *ImportHandler) Get(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	job, err := h.svc.Get(c.Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, job)
}

// Report streams the per-asset result CSV (created POs + assets with tags,
// plus skipped/errored rows from the job).
func (h *ImportHandler) Report(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := h.svc.Report(c.Context(), id, h.assets, h.pos, h.venues, h.categories, h.users, &buf); err != nil {
		return err
	}
	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="import-report-`+id.Hex()+`.csv"`)
	return c.Status(fiber.StatusOK).Send(buf.Bytes())
}
