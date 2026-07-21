//go:build integration

package service

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/database"
	"imp/internal/models"
	"imp/internal/repository"
)

func newReportSvc(t *testing.T) (*ReportService, *database.Mongo) {
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
	svc := NewReportService(
		repository.NewAssetRepository(conn.DB),
		repository.NewVenueRepository(conn.DB),
		repository.NewUserRepository(conn.DB),
		repository.NewRepairRepository(conn.DB),
		repository.NewDepartmentRepository(conn.DB),
	)
	return svc, conn
}

func TestReportServiceIT_AssetsAwayCursorTraversal(t *testing.T) {
	svc, conn := newReportSvc(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const n = 12
	for i := 0; i < n; i++ {
		a := models.Asset{
			ID: bson.NewObjectID(), AssetTag: fmt.Sprintf("A-%d", i), Name: "x",
			CategoryID: bson.NewObjectID(), HomeVenueID: bson.NewObjectID(),
			CurrentVenueID: bson.NewObjectID(), Status: models.Available,
			Condition: models.Good, IsActive: true, QrToken: fmt.Sprintf("q-%d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Second), UpdatedAt: base,
		}
		if _, err := conn.DB.Collection("assets").InsertOne(context.Background(), a); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Page 1
	rows, next, hasMore, err := svc.AssetsAway(context.Background(), nil, 5)
	if err != nil {
		t.Fatalf("AssetsAway p1: %v", err)
	}
	if len(rows) != 5 || !hasMore || next == nil {
		t.Fatalf("p1: rows=%d hasMore=%v next=%v", len(rows), hasMore, next)
	}
	// Page 2 (feed next)
	rows2, _, _, err := svc.AssetsAway(context.Background(), next, 5)
	if err != nil {
		t.Fatalf("AssetsAway p2: %v", err)
	}
	// No overlap between page 1 and page 2.
	seen := map[bson.ObjectID]bool{}
	for _, a := range rows {
		seen[a.ID] = true
	}
	for _, a := range rows2 {
		if seen[a.ID] {
			t.Fatalf("page 2 overlaps page 1 at %s", a.ID.Hex())
		}
	}

	// Walk to the end; last page has hasMore=false and next=nil.
	total := len(rows)
	cur := next
	for {
		r, nx, more, err := svc.AssetsAway(context.Background(), cur, 5)
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		total += len(r)
		if !more {
			if nx != nil {
				t.Fatalf("last page returned non-nil next cursor")
			}
			break
		}
		cur = nx
	}
	if total != n {
		t.Fatalf("walked %d assets, want %d", total, n)
	}
}
