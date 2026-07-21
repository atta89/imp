//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
	"imp/internal/pagination"
)

func seedRepair(t *testing.T, repo *RepairRepository, status models.RepairStatus, createdAt time.Time) bson.ObjectID {
	t.Helper()
	rep := &models.Repair{
		AssetID:    bson.NewObjectID(),
		Issue:      "broken",
		ReportedBy: bson.NewObjectID(),
		Status:     status,
		CreatedAt:  createdAt,
		ReportedAt: createdAt,
	}
	if err := repo.Create(context.Background(), rep); err != nil {
		t.Fatalf("create repair: %v", err)
	}
	return rep.ID
}

func TestRepairFindPageIT_FilterAndCoverage(t *testing.T) {
	conn := newTestDB(t) // reuse helper from asset_findpage_integration_test.go (same package)
	repo := NewRepairRepository(conn.DB)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	openFilter := bson.M{"status": bson.M{"$in": []models.RepairStatus{models.Open, models.InProgress}}}

	const nOpen = 25
	want := make(map[bson.ObjectID]struct{}, nOpen)
	for i := 0; i < nOpen; i++ {
		st := models.Open
		if i%2 == 1 {
			st = models.InProgress
		}
		want[seedRepair(t, repo, st, base.Add(time.Duration(i)*time.Second))] = struct{}{}
	}
	// Completed/unrepairable repairs must be excluded by the filter.
	for i := 0; i < 5; i++ {
		seedRepair(t, repo, models.Completed, base.Add(time.Duration(100+i)*time.Second))
	}

	var got []bson.ObjectID
	var after *pagination.Cursor
	for {
		rows, hasMore, err := repo.FindPage(context.Background(), openFilter, after, 8)
		if err != nil {
			t.Fatalf("FindPage: %v", err)
		}
		for _, r := range rows {
			got = append(got, r.ID)
		}
		if !hasMore {
			break
		}
		last := rows[len(rows)-1]
		after = &pagination.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if len(got) != nOpen {
		t.Fatalf("got %d open/in_progress repairs, want %d (closed leaked?)", len(got), nOpen)
	}
	for _, id := range got {
		if _, ok := want[id]; !ok {
			t.Fatalf("unexpected repair id %s", id.Hex())
		}
	}
}
