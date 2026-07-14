//go:build integration

// Integration test for the asset keyset-pagination primitives (CountUpTo,
// FindIDsAfter) that back the async ids-export bulk job. Requires MongoDB
// reachable via MONGO_URI, e.g.:
//
//	MONGO_URI='mongodb://localhost:27077/?replicaSet=rs0test' \
//	    go test -tags integration ./internal/repository/ -run TestAssetKeysetIT -count=1
//
// Runs against a freshly-created, uniquely-named database
// (imp_integration_test_<ns>) that is DROPPED on cleanup — the real `imp`
// database is never touched.
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
)

func TestAssetKeysetIT_ScanOrderAndCount(t *testing.T) {
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI not set")
	}
	ctx := context.Background()
	dbName := fmt.Sprintf("imp_integration_test_%d", time.Now().UnixNano())
	conn, err := database.Connect(ctx, uri, dbName)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.EnsureIndexes(ctx, conn.DB); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	t.Cleanup(func() { _ = conn.DB.Drop(context.Background()); _ = conn.Close(context.Background()) })

	repo := NewAssetRepository(conn.DB)
	const n = 2500
	want := make(map[bson.ObjectID]struct{}, n)
	for i := 0; i < n; i++ {
		a := &models.Asset{
			Name:           fmt.Sprintf("a-%d", i),
			Status:         models.Available,
			AssetTag:       fmt.Sprintf("TAG-%d-%d", time.Now().UnixNano(), i),
			QrToken:        fmt.Sprintf("QR-%d-%d", time.Now().UnixNano(), i),
			CategoryID:     bson.NewObjectID(),
			HomeVenueID:    bson.NewObjectID(),
			CurrentVenueID: bson.NewObjectID(),
			IsActive:       true,
		}
		if err := repo.Create(ctx, a); err != nil {
			t.Fatalf("seed: %v", err)
		}
		want[a.ID] = struct{}{}
	}

	// Keyset scan in batches of 1000.
	got := make(map[bson.ObjectID]struct{}, n)
	var last *bson.ObjectID
	var prev bson.ObjectID
	first := true
	for {
		batch, err := repo.FindIDsAfter(ctx, bson.M{}, last, 1000)
		if err != nil {
			t.Fatalf("FindIDsAfter: %v", err)
		}
		if len(batch) == 0 {
			break
		}
		for _, id := range batch {
			if !first && !(prev.Hex() < id.Hex()) {
				t.Fatalf("not strictly ascending: %s then %s", prev.Hex(), id.Hex())
			}
			if _, dup := got[id]; dup {
				t.Fatalf("duplicate id %s", id.Hex())
			}
			got[id] = struct{}{}
			prev = id
			first = false
		}
		lc := batch[len(batch)-1]
		last = &lc
		if len(batch) < 1000 {
			break
		}
	}
	if len(got) != n {
		t.Fatalf("scanned %d ids, want %d (gaps or overlap)", len(got), n)
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Fatalf("scan missed seeded id %s", id.Hex())
		}
	}

	// CountUpTo caps.
	c, err := repo.CountUpTo(ctx, bson.M{}, 100)
	if err != nil {
		t.Fatalf("CountUpTo: %v", err)
	}
	if c != 100 {
		t.Fatalf("CountUpTo(limit=100) = %d, want 100", c)
	}
}
