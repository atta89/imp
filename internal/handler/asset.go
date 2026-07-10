package handler

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/middleware"
	"imp/internal/models"
	"imp/internal/service"
	"imp/pkg/response"
)

type AssetHandler struct {
	assets *service.AssetService
}

func NewAssetHandler(assets *service.AssetService) *AssetHandler {
	return &AssetHandler{assets: assets}
}

func (h *AssetHandler) Create(c *fiber.Ctx) error {
	var req models.CreateAssetRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	if !middleware.IsAdmin(c) && !middleware.CanAccessVenue(c, req.HomeVenueID.Hex()) {
		return apperror.Forbidden("cannot create asset for that venue")
	}
	a, err := h.assets.Create(c.Context(), req)
	if err != nil {
		return err
	}
	return response.Created(c, a)
}

func (h *AssetHandler) List(c *fiber.Ctx) error {
	page, limit := ParsePagination(c)
	q, err := parseAssetListQuery(c)
	if err != nil {
		return err
	}
	// Non-admins see only assets whose home OR current venue is in their scope.
	if !middleware.IsAdmin(c) {
		ids, perr := parseHexIDs(middleware.CurrentVenueIDs(c))
		if perr != nil {
			return perr
		}
		q.Scope = ids
	}
	assets, total, err := h.assets.List(c.Context(), q, page, limit)
	if err != nil {
		return err
	}
	return response.Paginated(c, assets, response.Pagination{
		Page: page, Limit: limit, Total: total, TotalPages: TotalPages(total, limit),
	})
}

func (h *AssetHandler) Get(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	a, err := h.assets.Get(c.Context(), id)
	if err != nil {
		return err
	}
	if !canAccessAsset(c, a) {
		return apperror.Forbidden("not authorized for this asset")
	}
	return response.OK(c, a)
}

func (h *AssetHandler) Update(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	existing, err := h.assets.Get(c.Context(), id)
	if err != nil {
		return err
	}
	if !canAccessAsset(c, existing) {
		return apperror.Forbidden("not authorized for this asset")
	}
	var req models.UpdateAssetRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	a, err := h.assets.Update(c.Context(), id, req)
	if err != nil {
		return err
	}
	return response.OK(c, a)
}

func (h *AssetHandler) Delete(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	if err := h.assets.Delete(c.Context(), id); err != nil {
		return err
	}
	return response.NoContent(c)
}

func (h *AssetHandler) ChangeStatus(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	existing, err := h.assets.Get(c.Context(), id)
	if err != nil {
		return err
	}
	if !canAccessAsset(c, existing) {
		return apperror.Forbidden("not authorized for this asset")
	}
	var req models.StatusChangeRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	a, err := h.assets.ChangeStatus(c.Context(), id, middleware.CurrentUserID(c), req)
	if err != nil {
		return handleActionError(c, err)
	}
	return response.OK(c, a)
}

func (h *AssetHandler) Transfer(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	existing, err := h.assets.Get(c.Context(), id)
	if err != nil {
		return err
	}
	if !canAccessAsset(c, existing) {
		return apperror.Forbidden("not authorized for this asset")
	}
	var req models.TransferAssetRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	// Non-admins must also have access to the destination venue.
	if !middleware.IsAdmin(c) && !middleware.CanAccessVenue(c, req.ToVenueID.Hex()) {
		return apperror.Forbidden("not authorized for destination venue")
	}
	a, err := h.assets.Transfer(c.Context(), id, middleware.CurrentUserID(c), req)
	if err != nil {
		return handleActionError(c, err)
	}
	return response.OK(c, a)
}

func (h *AssetHandler) UpdateCondition(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	existing, err := h.assets.Get(c.Context(), id)
	if err != nil {
		return err
	}
	if !canAccessAsset(c, existing) {
		return apperror.Forbidden("not authorized for this asset")
	}
	var req models.ConditionUpdate
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	a, err := h.assets.UpdateCondition(c.Context(), id, middleware.CurrentUserID(c), req)
	if err != nil {
		return handleActionError(c, err)
	}
	return response.OK(c, a)
}

func (h *AssetHandler) Assign(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	existing, err := h.assets.Get(c.Context(), id)
	if err != nil {
		return err
	}
	if !canAccessAsset(c, existing) {
		return apperror.Forbidden("not authorized for this asset")
	}
	var req models.AssignCustodyRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	a, err := h.assets.AssignCustody(c.Context(), id, middleware.CurrentUserID(c), req)
	if err != nil {
		return handleActionError(c, err)
	}
	return response.OK(c, a)
}

// handleActionError renders an *AttachmentValidationFailure as the 400
// AttachmentValidationResponse body per §5 of the design; any other error
// is passed through unchanged for the standard error middleware to handle.
func handleActionError(c *fiber.Ctx, err error) error {
	var attErr *service.AttachmentValidationFailure
	if errors.As(err, &attErr) {
		return c.Status(fiber.StatusBadRequest).JSON(models.AttachmentValidationResponse{
			Error: models.ErrorPayload{
				Kind:    models.ErrorPayloadKindBadRequest,
				Message: "attachment validation failed",
			},
			Attachments: &attErr.PerAttachment,
		})
	}
	return err
}

func (h *AssetHandler) History(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	a, err := h.assets.Get(c.Context(), id)
	if err != nil {
		return err
	}
	if !canAccessAsset(c, a) {
		return apperror.Forbidden("not authorized for this asset")
	}
	movements, err := h.assets.History(c.Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, movements)
}

// QR returns a PNG of the asset's QR code.
func (h *AssetHandler) QR(c *fiber.Ctx) error {
	id, err := ParseObjectID(c, "id")
	if err != nil {
		return err
	}
	existing, err := h.assets.Get(c.Context(), id)
	if err != nil {
		return err
	}
	if !canAccessAsset(c, existing) {
		return apperror.Forbidden("not authorized for this asset")
	}
	png, err := h.assets.QRPNG(c.Context(), id)
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderContentType, "image/png")
	c.Set(fiber.HeaderContentDisposition, `inline; filename="`+existing.AssetTag+`.png"`)
	return c.Send(png)
}

// QRBulk returns a multi-page PDF of QR labels for the requested assets.
func (h *AssetHandler) QRBulk(c *fiber.Ctx) error {
	var req models.BulkQrRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	pdf, err := h.assets.QRBulkPDF(c.Context(), principal(c), req.AssetIDs)
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderContentType, "application/pdf")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="asset-labels.pdf"`)
	return c.Send(pdf)
}

// canAccessAsset checks the requester is either admin or has the asset's home
// or current venue in their scope. This is the venue-scope-only gate used by
// the asset-management endpoints (GET/PUT /assets/:id, actions, repairs); the
// authenticated scan and attachment-download paths use the custody-aware
// service.Principal.CanAccessAsset instead.
func canAccessAsset(c *fiber.Ctx, a *models.Asset) bool {
	if middleware.IsAdmin(c) {
		return true
	}
	return middleware.CanAccessVenue(c, a.HomeVenueID.Hex()) ||
		middleware.CanAccessVenue(c, a.CurrentVenueID.Hex())
}

func parseAssetListQuery(c *fiber.Ctx) (service.AssetListQuery, error) {
	q := service.AssetListQuery{}

	if v := c.Query("venue"); v != "" {
		id, err := bson.ObjectIDFromHex(v)
		if err != nil {
			return q, apperror.BadRequest("invalid venue id")
		}
		q.Venue = &id
	}
	if v := c.Query("currentVenue"); v != "" {
		id, err := bson.ObjectIDFromHex(v)
		if err != nil {
			return q, apperror.BadRequest("invalid currentVenue id")
		}
		q.CurrentVenue = &id
	}
	if v := c.Query("category"); v != "" {
		id, err := bson.ObjectIDFromHex(v)
		if err != nil {
			return q, apperror.BadRequest("invalid category id")
		}
		q.Category = &id
	}
	if v := c.Query("department"); v != "" {
		id, err := bson.ObjectIDFromHex(v)
		if err != nil {
			return q, apperror.BadRequest("invalid department id")
		}
		q.Department = &id
	}
	if v := c.Query("responsible"); v != "" {
		id, err := bson.ObjectIDFromHex(v)
		if err != nil {
			return q, apperror.BadRequest("invalid responsible id")
		}
		q.Responsible = &id
	}
	if v := c.Query("status"); v != "" {
		q.Status = models.AssetStatus(v)
	}
	q.Away = parseBoolQuery(c.Query("away"))
	q.Overdue = parseBoolQuery(c.Query("overdue"))
	q.Q = strings.TrimSpace(c.Query("q"))
	return q, nil
}

func parseBoolQuery(s string) bool {
	switch strings.ToLower(s) {
	case "true", "1", "yes":
		return true
	}
	return false
}

func (h *AssetHandler) BulkTransfer(c *fiber.Ctx) error {
	var req models.BulkTransferRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	resp, err := h.assets.BulkTransfer(c.Context(), middleware.CurrentUserID(c), principal(c), req)
	if err != nil {
		return handleActionError(c, err)
	}
	return response.OK(c, resp)
}

func (h *AssetHandler) BulkStatus(c *fiber.Ctx) error {
	var req models.BulkStatusRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	resp, err := h.assets.BulkChangeStatus(c.Context(), middleware.CurrentUserID(c), principal(c), req)
	if err != nil {
		return handleActionError(c, err)
	}
	return response.OK(c, resp)
}

func (h *AssetHandler) BulkAssign(c *fiber.Ctx) error {
	var req models.BulkAssignRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	resp, err := h.assets.BulkAssign(c.Context(), middleware.CurrentUserID(c), principal(c), req)
	if err != nil {
		return handleActionError(c, err)
	}
	return response.OK(c, resp)
}

func (h *AssetHandler) BulkCondition(c *fiber.Ctx) error {
	var req models.BulkConditionUpdate
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	resp, err := h.assets.BulkUpdateCondition(c.Context(), middleware.CurrentUserID(c), principal(c), req)
	if err != nil {
		return handleActionError(c, err)
	}
	return response.OK(c, resp)
}

// principal flattens the authenticated request principal (identity, role,
// venue scope) into a service.Principal for the fiber-free authorization
// helpers. Shared by the bulk asset actions and the authenticated scan path.
func principal(c *fiber.Ctx) service.Principal {
	venues := middleware.CurrentVenueIDs(c)
	set := make(map[string]struct{}, len(venues))
	for _, v := range venues {
		set[v] = struct{}{}
	}
	return service.Principal{
		IsAdmin:  middleware.IsAdmin(c),
		UserID:   middleware.CurrentUserID(c),
		VenueIDs: set,
	}
}
