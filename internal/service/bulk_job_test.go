package service

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
	"imp/internal/repository"
)

func svc(cfg BulkJobConfig) *BulkJobService {
	return &BulkJobService{cfg: cfg}
}

func TestDedupeAndCap(t *testing.T) {
	s := svc(BulkJobConfig{MaxAssets: 3})
	a, b, c, d := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()

	t.Run("dedupes preserving first-occurrence order", func(t *testing.T) {
		got, err := s.dedupeAndCap([]bson.ObjectID{a, b, a, c, b})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		want := []bson.ObjectID{a, b, c}
		if len(got) != len(want) {
			t.Fatalf("len = %d, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("at %d: got %s want %s", i, got[i].Hex(), want[i].Hex())
			}
		}
	})

	t.Run("empty is 400", func(t *testing.T) {
		if _, err := s.dedupeAndCap(nil); err == nil {
			t.Fatal("expected error for empty batch")
		}
	})

	t.Run("over-cap is 400 (measured after dedupe)", func(t *testing.T) {
		// 4 distinct ids, cap 3 → over cap.
		if _, err := s.dedupeAndCap([]bson.ObjectID{a, b, c, d}); err == nil {
			t.Fatal("expected over-cap error")
		}
		// 4 ids but only 3 distinct → within cap.
		if _, err := s.dedupeAndCap([]bson.ObjectID{a, b, c, a}); err != nil {
			t.Fatalf("dedup should bring within cap: %v", err)
		}
	})
}

func TestCapErrors(t *testing.T) {
	mk := func(n int) []models.BulkJobRowError {
		out := make([]models.BulkJobRowError, n)
		for i := range out {
			out[i] = models.BulkJobRowError{AssetID: bson.NewObjectID(), Code: "x", Message: "x"}
		}
		return out
	}

	t.Run("nil becomes non-nil empty", func(t *testing.T) {
		got, trunc := capErrors(nil, 10)
		if got == nil {
			t.Fatal("errors must never be nil (required API array)")
		}
		if trunc {
			t.Fatal("no truncation expected")
		}
	})

	t.Run("truncates over cap", func(t *testing.T) {
		got, trunc := capErrors(mk(15), 10)
		if len(got) != 10 || !trunc {
			t.Fatalf("len=%d trunc=%v, want 10/true", len(got), trunc)
		}
	})

	t.Run("under cap untouched", func(t *testing.T) {
		got, trunc := capErrors(mk(5), 10)
		if len(got) != 5 || trunc {
			t.Fatalf("len=%d trunc=%v, want 5/false", len(got), trunc)
		}
	})
}

func TestTerminalStatus(t *testing.T) {
	cases := []struct {
		name string
		c    models.BulkJobCounts
		want models.BulkJobStatus
	}{
		{"all succeeded", models.BulkJobCounts{Total: 5, Succeeded: 5}, models.BulkJobStatusCompleted},
		{"partial errors", models.BulkJobCounts{Total: 5, Succeeded: 3, Failed: 2}, models.BulkJobStatusCompletedWithErrors},
		{"zero success zero skip", models.BulkJobCounts{Total: 5, Failed: 5}, models.BulkJobStatusFailed},
		{"all skipped (no-ops) completes, not fails", models.BulkJobCounts{Total: 5, Skipped: 5}, models.BulkJobStatusCompleted},
		{"success + skips, no errors", models.BulkJobCounts{Total: 5, Succeeded: 2, Skipped: 3}, models.BulkJobStatusCompleted},
		{"success + skips + errors", models.BulkJobCounts{Total: 6, Succeeded: 2, Skipped: 3, Failed: 1}, models.BulkJobStatusCompletedWithErrors},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalStatus(tc.c); got != tc.want {
				t.Fatalf("terminalStatus = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestRowErrorsFromAndGlobalFailure(t *testing.T) {
	a, b := bson.NewObjectID(), bson.NewObjectID()
	nf, forb := "not_found", "forbidden"
	results := []models.BulkActionResult{
		{AssetID: a, Ok: true},
		{AssetID: b, Ok: false, Error: &nf},
		{AssetID: bson.NilObjectID, Ok: false, Error: &forb}, // synthetic global row
	}

	if !hasGlobalFailure(results) {
		t.Fatal("expected a global (NilObjectID) failure to be detected")
	}

	errs := rowErrorsFrom(results)
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 row error (global row excluded), got %d", len(errs))
	}
	if errs[0].AssetID != b || errs[0].Code != "not_found" {
		t.Fatalf("unexpected row error: %+v", errs[0])
	}
}

func TestNewJobDocChunkingMath(t *testing.T) {
	s := svc(BulkJobConfig{BatchSize: 100, ErrorCap: 1000})
	ids := make([]bson.ObjectID, 250)
	for i := range ids {
		ids[i] = bson.NewObjectID()
	}
	job := s.newJobDoc(models.BulkJobTypeStatus, bson.NewObjectID(), ids, nil, false, repository.BulkJobParams{}, 250, 0, 0, nil)

	if job.Progress.BatchesTotal != 3 { // ceil(250/100)
		t.Fatalf("batchesTotal = %d, want 3", job.Progress.BatchesTotal)
	}
	if job.Counts.Total != 250 {
		t.Fatalf("total = %d, want 250", job.Counts.Total)
	}
	if job.Status != models.BulkJobStatusQueued {
		t.Fatalf("status = %s, want queued", job.Status)
	}
	if job.Errors == nil {
		t.Fatal("errors slice must be non-nil")
	}
	if job.Cursor != 0 {
		t.Fatalf("cursor = %d, want 0", job.Cursor)
	}
}
