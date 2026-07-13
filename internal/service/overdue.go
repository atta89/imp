package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
	"imp/internal/notification"
	"imp/internal/repository"
)

type OverdueService struct {
	assets  *repository.AssetRepository
	users   *repository.UserRepository
	venues  *repository.VenueRepository
	outbox  *notification.Repository
	logger  *slog.Logger
	baseURL string
	clock   func() time.Time
}

func NewOverdueService(
	assets *repository.AssetRepository,
	users *repository.UserRepository,
	venues *repository.VenueRepository,
	outbox *notification.Repository,
	logger *slog.Logger,
	baseURL string,
) *OverdueService {
	return &OverdueService{
		assets:  assets,
		users:   users,
		venues:  venues,
		outbox:  outbox,
		logger:  logger,
		baseURL: strings.TrimRight(baseURL, "/"),
		clock:   time.Now,
	}
}

// ScanResult is what the cron job logs after a daily scan.
type ScanResult struct {
	NewlyFlagged  int64
	Recipients    int
	DigestsQueued int
	AssetsCovered int
}

// RunDailyScan flags newly-overdue assets and enqueues one digest email per
// responsible person (PRD §6.5, §6.11). NEVER emails managers/admins about
// overdue items — digest goes to the asset's custodian only.
func (s *OverdueService) RunDailyScan(ctx context.Context) (ScanResult, error) {
	now := s.clock().UTC()

	flagged, err := s.assets.FlagOverdue(ctx, now)
	if err != nil {
		return ScanResult{}, err
	}

	overdue, err := s.assets.FindOverdueWithCustodian(ctx)
	if err != nil {
		return ScanResult{}, err
	}
	grouped := groupOverdueByCustodian(overdue)

	venueIDs := collectVenueIDs(overdue)
	venueNames, err := s.batchVenueNames(ctx, venueIDs)
	if err != nil {
		return ScanResult{}, err
	}

	result := ScanResult{
		NewlyFlagged:  flagged,
		Recipients:    len(grouped),
		AssetsCovered: len(overdue),
	}

	for uid, items := range grouped {
		u, err := s.users.FindByID(ctx, uid)
		if err != nil {
			s.logger.Warn("overdue_digest_user_missing", slog.String("userId", uid.Hex()))
			continue
		}
		if !u.IsActive || !u.NotifyByEmail {
			continue
		}
		subject, body := composeOverdueDigest(u, items, venueNames, s.baseURL, now)
		n := &models.Notification{
			Type:            models.Overdue,
			RecipientUserID: u.ID,
			RecipientEmail:  u.Email,
			Subject:         subject,
			Body:            body,
		}
		if err := s.outbox.Create(ctx, n); err != nil {
			s.logger.Error("overdue_digest_enqueue_failed",
				slog.String("userId", uid.Hex()),
				slog.Any("err", err),
			)
			continue
		}
		result.DigestsQueued++
	}

	s.logger.Info("overdue_scan_complete",
		slog.Int64("newly_flagged", result.NewlyFlagged),
		slog.Int("recipients", result.Recipients),
		slog.Int("digests_queued", result.DigestsQueued),
		slog.Int("assets_covered", result.AssetsCovered),
	)
	return result, nil
}

// groupOverdueByCustodian buckets assets by responsibleUserId. Assets with
// no custodian are dropped — there's no one to send a digest to.
func groupOverdueByCustodian(assets []models.Asset) map[bson.ObjectID][]models.Asset {
	out := map[bson.ObjectID][]models.Asset{}
	for _, a := range assets {
		if a.ResponsibleUserID == nil {
			continue
		}
		out[*a.ResponsibleUserID] = append(out[*a.ResponsibleUserID], a)
	}
	return out
}

// collectVenueIDs returns the distinct set of current-venue IDs across a list
// of overdue assets — used for a single batched venue-name lookup.
func collectVenueIDs(assets []models.Asset) []bson.ObjectID {
	seen := map[bson.ObjectID]struct{}{}
	for _, a := range assets {
		seen[a.CurrentVenueID] = struct{}{}
	}
	out := make([]bson.ObjectID, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}

func (s *OverdueService) batchVenueNames(ctx context.Context, ids []bson.ObjectID) (map[bson.ObjectID]string, error) {
	venues, err := s.venues.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[bson.ObjectID]string, len(venues))
	for _, v := range venues {
		out[v.ID] = v.Name
	}
	return out, nil
}

// composeOverdueDigest renders the subject + plain-text body for one user.
// Pure function — tested directly.
func composeOverdueDigest(
	u *models.User,
	assets []models.Asset,
	venueNames map[bson.ObjectID]string,
	baseURL string,
	asOf time.Time,
) (subject, body string) {
	// Stable order: oldest expectedReturnDate first.
	sorted := append([]models.Asset(nil), assets...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ti := timeOrZero(sorted[i].ExpectedReturnDate)
		tj := timeOrZero(sorted[j].ExpectedReturnDate)
		return ti.Before(tj)
	})

	n := len(sorted)
	subject = fmt.Sprintf("%d asset(s) overdue for return", n)

	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\n", u.Name)
	fmt.Fprintf(&b, "As of %s, the following %d asset(s) for which you are responsible are past their expected return date:\n\n",
		asOf.Format("2006-01-02"), n)

	base := strings.TrimRight(baseURL, "/")
	for _, a := range sorted {
		venue := venueNames[a.CurrentVenueID]
		if venue == "" {
			venue = a.CurrentVenueID.Hex()
		}
		expected := "(no date)"
		if a.ExpectedReturnDate != nil {
			expected = a.ExpectedReturnDate.Format("2006-01-02")
		}
		fmt.Fprintf(&b, "- %s — %s\n    expected back: %s\n    currently at:  %s\n    view: %s/scan/%s\n",
			a.AssetTag, a.Name, expected, venue, base, a.QrToken)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Please follow up to return them or extend the expected return date.")
	return subject, b.String()
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
