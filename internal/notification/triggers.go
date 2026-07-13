package notification

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
	"imp/internal/repository"
)

// Triggers is the surface the service layer uses to enqueue notifications
// without knowing about email, SMTP, or templates. Failures are logged but
// never returned — a transient email enqueue should not fail the parent
// business operation.
type Triggers struct {
	outbox  *Repository
	users   *repository.UserRepository
	venues  *repository.VenueRepository
	logger  *slog.Logger
	baseURL string
}

func NewTriggers(outbox *Repository, users *repository.UserRepository, venues *repository.VenueRepository, logger *slog.Logger, baseURL string) *Triggers {
	return &Triggers{
		outbox:  outbox,
		users:   users,
		venues:  venues,
		logger:  logger,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

func (t *Triggers) scanURL(token string) string {
	return t.baseURL + "/scan/" + token
}

// CustodyAssigned emails the new responsible person that they're now
// accountable for this asset (PRD §6.7, §6.11).
func (t *Triggers) CustodyAssigned(ctx context.Context, asset *models.Asset, newCustodianID bson.ObjectID) {
	u, err := t.users.FindByID(ctx, newCustodianID)
	if err != nil {
		t.logger.Warn("custody_trigger_user_lookup_failed", slog.Any("err", err))
		return
	}
	if !u.IsActive || !u.NotifyByEmail {
		return
	}
	subject := fmt.Sprintf("You're now responsible for asset %s", asset.AssetTag)
	body := fmt.Sprintf(
		"Hi %s,\n\nYou've been assigned as the responsible person for asset %s (%s).\n\nView: %s\n",
		u.Name, asset.AssetTag, asset.Name, t.scanURL(asset.QrToken),
	)
	t.enqueue(ctx, &models.Notification{
		Type:            models.CustodyChange,
		RecipientUserID: u.ID,
		RecipientEmail:  u.Email,
		AssetID:         &asset.ID,
		Subject:         subject,
		Body:            body,
	})
}

// AssetTransferred emails every venue manager assigned to the asset's HOME
// venue that the asset has moved (PRD §6.11). Multiple managers → multiple
// outbox entries, one per recipient.
func (t *Triggers) AssetTransferred(ctx context.Context, asset *models.Asset, fromVenueID, toVenueID bson.ObjectID) {
	managers, err := t.users.FindManagersForVenue(ctx, asset.HomeVenueID)
	if err != nil {
		t.logger.Warn("transfer_trigger_manager_lookup_failed", slog.Any("err", err))
		return
	}
	if len(managers) == 0 {
		return
	}
	toName := venueLabel(ctx, t.venues, toVenueID)
	fromName := venueLabel(ctx, t.venues, fromVenueID)
	subject := fmt.Sprintf("Asset %s moved from %s to %s", asset.AssetTag, fromName, toName)
	link := t.scanURL(asset.QrToken)

	for i := range managers {
		m := &managers[i]
		if !m.NotifyByEmail {
			continue
		}
		body := fmt.Sprintf(
			"Hi %s,\n\nAsset %s (%s) — which is based at your venue — has been moved from %s to %s.\n\nView: %s\n",
			m.Name, asset.AssetTag, asset.Name, fromName, toName, link,
		)
		t.enqueue(ctx, &models.Notification{
			Type:            models.Transfer,
			RecipientUserID: m.ID,
			RecipientEmail:  m.Email,
			AssetID:         &asset.ID,
			Subject:         subject,
			Body:            body,
		})
	}
}

// RepairClosed emails the asset's current custodian and the original reporter
// (deduplicated) when a repair completes or is marked unrepairable.
func (t *Triggers) RepairClosed(ctx context.Context, asset *models.Asset, repair *models.Repair) {
	recipients := map[bson.ObjectID]struct{}{}
	if asset.ResponsibleUserID != nil {
		recipients[*asset.ResponsibleUserID] = struct{}{}
	}
	recipients[repair.ReportedBy] = struct{}{}

	var verdict string
	switch repair.Status {
	case models.Completed:
		verdict = "completed"
	case models.Unrepairable:
		verdict = "marked unrepairable (asset retired)"
	default:
		return // not actually closed
	}
	subject := fmt.Sprintf("Repair for asset %s %s", asset.AssetTag, verdict)
	resolution := ""
	if repair.Resolution != nil && *repair.Resolution != "" {
		resolution = "\nResolution: " + *repair.Resolution
	}
	link := t.scanURL(asset.QrToken)

	for id := range recipients {
		u, err := t.users.FindByID(ctx, id)
		if err != nil || !u.IsActive || !u.NotifyByEmail {
			continue
		}
		body := fmt.Sprintf(
			"Hi %s,\n\nThe repair for asset %s (%s) has been %s.%s\n\nView: %s\n",
			u.Name, asset.AssetTag, asset.Name, verdict, resolution, link,
		)
		t.enqueue(ctx, &models.Notification{
			Type:            models.RepairUpdate,
			RecipientUserID: u.ID,
			RecipientEmail:  u.Email,
			AssetID:         &asset.ID,
			Subject:         subject,
			Body:            body,
		})
	}
}

func (t *Triggers) enqueue(ctx context.Context, n *models.Notification) {
	if err := t.outbox.Create(ctx, n); err != nil {
		t.logger.Error("enqueue_notification_failed",
			slog.Any("err", err),
			slog.String("type", string(n.Type)),
			slog.String("recipient", string(n.RecipientEmail)),
		)
	}
}

func venueLabel(ctx context.Context, venues *repository.VenueRepository, id bson.ObjectID) string {
	v, err := venues.FindByID(ctx, id)
	if err != nil {
		return id.Hex()
	}
	return v.Name
}

// BulkTransferAssetRef is the per-asset row in a bulk transfer digest.
type BulkTransferAssetRef struct {
	AssetID bson.ObjectID
	Tag     string
	Name    string
	QRToken string
}

// BulkTransferGroup pairs a home venue with the assets the digest must list.
type BulkTransferGroup struct {
	HomeVenueID bson.ObjectID
	Assets      []BulkTransferAssetRef
}

// BulkCustodyAssignedRef is the per-asset row in a bulk custody-assign digest.
type BulkCustodyAssignedRef struct {
	AssetID   bson.ObjectID
	Tag       string
	Name      string
	VenueName string
	QRToken   string
}

// BulkCustodyAssignedDigest enqueues exactly one email to the new custodian
// listing every asset they were just assigned. A per-asset fan-out would blow
// Gmail's daily quota on a 500-item batch; one digest keeps it to a single
// send regardless of size. NOP when the user is missing, inactive, or has
// NotifyByEmail=false.
func (t *Triggers) BulkCustodyAssignedDigest(ctx context.Context, newCustodianID bson.ObjectID, refs []BulkCustodyAssignedRef) {
	if len(refs) == 0 {
		return
	}
	u, err := t.users.FindByID(ctx, newCustodianID)
	if err != nil {
		t.logger.Warn("bulk_custody_trigger_user_lookup_failed", slog.Any("err", err))
		return
	}
	n := buildBulkCustodyAssignedNotification(u, refs, t.scanURL)
	if n == nil {
		return
	}
	t.enqueue(ctx, n)
}

// buildBulkCustodyAssignedNotification returns the outbox row for one bulk
// custody-assign digest, or nil when the recipient is missing/inactive/opted
// out. Pulled out of the trigger so the subject/body construction and the
// NotifyByEmail short-circuit can be unit-tested without a repository.
func buildBulkCustodyAssignedNotification(u *models.User, refs []BulkCustodyAssignedRef, scanURL func(string) string) *models.Notification {
	if u == nil || !u.IsActive || !u.NotifyByEmail {
		return nil
	}
	if len(refs) == 0 {
		return nil
	}
	var listing strings.Builder
	for _, r := range refs {
		fmt.Fprintf(&listing, "  • %s — %s @ %s (%s)\n", r.Tag, r.Name, r.VenueName, scanURL(r.QRToken))
	}
	subject := fmt.Sprintf("You're now responsible for %d asset(s)", len(refs))
	body := fmt.Sprintf(
		"Hi %s,\n\nYou've been assigned as the responsible person for the following assets:\n\n%s\n",
		u.Name, listing.String(),
	)
	return &models.Notification{
		Type:            models.CustodyChange,
		RecipientUserID: u.ID,
		RecipientEmail:  u.Email,
		Subject:         subject,
		Body:            body,
	}
}

// BulkTransferDigest enqueues one outbox row per home-venue manager listing
// every asset of theirs moved in this batch. Replaces the per-asset
// AssetTransferred fan-out for the bulk path.
func (t *Triggers) BulkTransferDigest(ctx context.Context, groups []BulkTransferGroup, toVenueID bson.ObjectID) {
	toName := venueLabel(ctx, t.venues, toVenueID)
	for _, g := range groups {
		managers, err := t.users.FindManagersForVenue(ctx, g.HomeVenueID)
		if err != nil {
			t.logger.Warn("bulk_transfer_trigger_manager_lookup_failed", slog.Any("err", err))
			continue
		}
		if len(managers) == 0 {
			continue
		}
		fromName := venueLabel(ctx, t.venues, g.HomeVenueID)
		var listing strings.Builder
		for _, a := range g.Assets {
			fmt.Fprintf(&listing, "  • %s — %s (%s)\n", a.Tag, a.Name, t.scanURL(a.QRToken))
		}
		subject := fmt.Sprintf("%d assets moved from %s to %s", len(g.Assets), fromName, toName)
		for i := range managers {
			m := &managers[i]
			if !m.NotifyByEmail {
				continue
			}
			body := fmt.Sprintf(
				"Hi %s,\n\nThe following assets — based at your venue — have been moved to %s:\n\n%s\n",
				m.Name, toName, listing.String(),
			)
			t.enqueue(ctx, &models.Notification{
				Type:            models.Transfer,
				RecipientUserID: m.ID,
				RecipientEmail:  m.Email,
				Subject:         subject,
				Body:            body,
			})
		}
	}
}
