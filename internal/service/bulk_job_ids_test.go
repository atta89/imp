package service

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
)

func idsSvc() *BulkJobService {
	return NewBulkJobService(nil, nil, nil, BulkJobConfig{IDsMaxLimit: 100, IDsBatchSize: 10}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func intp(i int) *int { return &i }

func TestEnqueueIDs_LimitOutOfRange(t *testing.T) {
	s := idsSvc()
	ctx := context.Background()
	p := Principal{IsAdmin: true, UserID: bson.NewObjectID()}

	if _, err := s.EnqueueIDs(ctx, p.UserID, p, models.BulkIdsRequest{Limit: intp(0)}); err == nil {
		t.Fatal("limit=0 should 400")
	}
	if _, err := s.EnqueueIDs(ctx, p.UserID, p, models.BulkIdsRequest{Limit: intp(101)}); err == nil {
		t.Fatal("limit above cap should 400")
	}
}

func TestEnqueueIDs_MalformedFilter(t *testing.T) {
	s := idsSvc()
	ctx := context.Background()
	p := Principal{IsAdmin: true, UserID: bson.NewObjectID()}
	bad := "nothex"
	_, err := s.EnqueueIDs(ctx, p.UserID, p, models.BulkIdsRequest{
		Filters: &models.AssetListFilters{Venue: &bad},
	})
	appErr, ok := apperror.As(err)
	if !ok || appErr.Kind != apperror.KindBadRequest || appErr.Message != "invalid venue id" {
		t.Fatalf("err = %v, want BadRequest 'invalid venue id'", err)
	}
}
