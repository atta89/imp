package service

import (
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
)

func ptrTime(t time.Time) *time.Time { return &t }
func ptrOID(id bson.ObjectID) *bson.ObjectID { return &id }

func TestGroupOverdueByCustodian(t *testing.T) {
	userA := bson.NewObjectID()
	userB := bson.NewObjectID()

	asset := func(custodian *bson.ObjectID, tag string) models.Asset {
		return models.Asset{AssetTag: tag, ResponsibleUserID: custodian}
	}

	cases := []struct {
		name     string
		in       []models.Asset
		expected map[bson.ObjectID]int // user -> count
	}{
		{
			name:     "empty input → empty map",
			in:       nil,
			expected: map[bson.ObjectID]int{},
		},
		{
			name: "single user, multiple assets",
			in: []models.Asset{
				asset(ptrOID(userA), "LAP-0001"),
				asset(ptrOID(userA), "LAP-0002"),
			},
			expected: map[bson.ObjectID]int{userA: 2},
		},
		{
			name: "assets with no custodian are dropped",
			in: []models.Asset{
				asset(ptrOID(userA), "LAP-0001"),
				asset(nil, "ORPHAN-0001"),
			},
			expected: map[bson.ObjectID]int{userA: 1},
		},
		{
			name: "multiple users",
			in: []models.Asset{
				asset(ptrOID(userA), "LAP-0001"),
				asset(ptrOID(userB), "TBL-0001"),
				asset(ptrOID(userA), "LAP-0002"),
				asset(ptrOID(userB), "TBL-0002"),
				asset(nil, "ORPHAN"),
			},
			expected: map[bson.ObjectID]int{userA: 2, userB: 2},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := groupOverdueByCustodian(c.in)
			if len(got) != len(c.expected) {
				t.Fatalf("got %d groups, want %d", len(got), len(c.expected))
			}
			for uid, want := range c.expected {
				if n := len(got[uid]); n != want {
					t.Errorf("user %s: got %d assets, want %d", uid.Hex(), n, want)
				}
			}
		})
	}
}

func TestComposeOverdueDigest_SubjectCountsAssets(t *testing.T) {
	user := &models.User{Name: "Pat", ID: bson.NewObjectID()}
	now := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	assets := []models.Asset{
		{AssetTag: "LAP-0001", Name: "MacBook", ExpectedReturnDate: ptrTime(now.AddDate(0, 0, -3))},
		{AssetTag: "LAP-0002", Name: "ThinkPad", ExpectedReturnDate: ptrTime(now.AddDate(0, 0, -1))},
	}
	subj, body := composeOverdueDigest(user, assets, nil, "https://app.example.com", now)
	if !strings.Contains(subj, "2 asset(s)") {
		t.Errorf("subject should mention 2 assets, got %q", subj)
	}
	if !strings.Contains(body, "Hi Pat,") {
		t.Errorf("body should greet by name, got: %q", body)
	}
	if !strings.Contains(body, "LAP-0001") || !strings.Contains(body, "LAP-0002") {
		t.Error("body should list both asset tags")
	}
}

func TestComposeOverdueDigest_SortedByOldestFirst(t *testing.T) {
	user := &models.User{Name: "Pat"}
	now := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -10)
	newer := now.AddDate(0, 0, -1)
	assets := []models.Asset{
		{AssetTag: "RECENT", ExpectedReturnDate: &newer},
		{AssetTag: "OLDEST", ExpectedReturnDate: &old},
	}
	_, body := composeOverdueDigest(user, assets, nil, "", now)
	iOld := strings.Index(body, "OLDEST")
	iRecent := strings.Index(body, "RECENT")
	if iOld < 0 || iRecent < 0 {
		t.Fatalf("body missing tags: %q", body)
	}
	if iOld > iRecent {
		t.Errorf("OLDEST should appear before RECENT (sorted by oldest expected date); body: %q", body)
	}
}

func TestComposeOverdueDigest_VenueNameFallbackToHex(t *testing.T) {
	user := &models.User{Name: "Pat"}
	unknownVenue := bson.NewObjectID()
	assets := []models.Asset{
		{AssetTag: "LAP-0001", CurrentVenueID: unknownVenue},
	}
	_, body := composeOverdueDigest(user, assets, map[bson.ObjectID]string{}, "", time.Now())
	if !strings.Contains(body, unknownVenue.Hex()) {
		t.Errorf("expected hex fallback for unknown venue, got: %q", body)
	}
}
