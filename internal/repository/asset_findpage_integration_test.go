//go:build integration

package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/database"
	"imp/internal/models"
	"imp/internal/pagination"
)

func newTestDB(t *testing.T) *database.Mongo {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI not set")
	}
	dbName := fmt.Sprintf("imp_integration_test_%d", time.Now().UnixNano())
	conn, err := database.Connect(context.Background(), uri, dbName)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.EnsureIndexes(context.Background(), conn.DB); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.DB.Drop(context.Background())
		_ = conn.Close(context.Background())
	})
	return conn
}

// seedAwayAsset inserts one asset with home != current (so it is "away").
// createdAt is base+offset so a larger offset sorts newer.
func seedAwayAsset(t *testing.T, conn *database.Mongo, base time.Time, offset int) bson.ObjectID {
	t.Helper()
	id := bson.NewObjectID()
	a := models.Asset{
		ID:             id,
		AssetTag:       fmt.Sprintf("AWAY-%d", offset),
		Name:           "away",
		CategoryID:     bson.NewObjectID(),
		HomeVenueID:    bson.NewObjectID(),
		CurrentVenueID: bson.NewObjectID(), // != home ⇒ away
		Status:         models.Available,
		Condition:      models.Good,
		IsActive:       true,
		QrToken:        fmt.Sprintf("qr-%d", offset),
		CreatedAt:      base.Add(time.Duration(offset) * time.Second),
		UpdatedAt:      base,
	}
	if _, err := conn.DB.Collection("assets").InsertOne(context.Background(), a); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	return id
}

// drainAway walks every page of FindAwayFromHomePage and returns ids in order.
func drainAway(t *testing.T, repo *AssetRepository, limit int) []bson.ObjectID {
	t.Helper()
	var got []bson.ObjectID
	var after *pagination.Cursor
	for {
		rows, hasMore, err := repo.FindAwayFromHomePage(context.Background(), after, limit)
		if err != nil {
			t.Fatalf("FindAwayFromHomePage: %v", err)
		}
		for _, a := range rows {
			got = append(got, a.ID)
		}
		if !hasMore {
			break
		}
		last := rows[len(rows)-1]
		after = &pagination.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
		if len(got) > 10000 {
			t.Fatal("pagination did not terminate")
		}
	}
	return got
}

func TestAssetFindPageIT_AwayCoverage(t *testing.T) {
	conn := newTestDB(t)
	repo := NewAssetRepository(conn.DB)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	const n = 57
	want := make(map[bson.ObjectID]struct{}, n)
	for i := 0; i < n; i++ {
		want[seedAwayAsset(t, conn, base, i)] = struct{}{}
	}

	got := drainAway(t, repo, 10)
	if len(got) != n {
		t.Fatalf("got %d ids, want %d", len(got), n)
	}
	seen := make(map[bson.ObjectID]struct{}, n)
	for _, id := range got {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %s across pages", id.Hex())
		}
		seen[id] = struct{}{}
		if _, ok := want[id]; !ok {
			t.Fatalf("unexpected id %s", id.Hex())
		}
	}
}

func TestAssetFindPageIT_TieBreak(t *testing.T) {
	conn := newTestDB(t)
	repo := NewAssetRepository(conn.DB)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// All 30 share the SAME createdAt (offset 0) ⇒ the _id tiebreak must keep
	// the scan gap-free across page boundaries.
	const n = 30
	want := make(map[bson.ObjectID]struct{}, n)
	for i := 0; i < n; i++ {
		want[seedAwayAsset(t, conn, base, 0)] = struct{}{}
	}
	got := drainAway(t, repo, 7)
	if len(got) != n {
		t.Fatalf("tie scan got %d ids, want %d", len(got), n)
	}
	for _, id := range got {
		if _, ok := want[id]; !ok {
			t.Fatalf("unexpected id %s", id.Hex())
		}
	}
}
