//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/database"
	"imp/internal/middleware"
	"imp/internal/models"
	"imp/internal/repository"
	"imp/internal/service"
	"imp/pkg/response"
)

type awayEnvelope struct {
	Data []models.Asset `json:"data"`
	Meta struct {
		Cursor *response.CursorPage `json:"cursor"`
	} `json:"meta"`
}

func TestReportHandlerIT_AssetsAwayPaging(t *testing.T) {
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
		t.Fatalf("indexes: %v", err)
	}
	t.Cleanup(func() { _ = conn.DB.Drop(ctx); _ = conn.Close(ctx) })

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const n = 7
	for i := 0; i < n; i++ {
		a := models.Asset{
			ID: bson.NewObjectID(), AssetTag: fmt.Sprintf("A-%d", i), Name: "x",
			CategoryID: bson.NewObjectID(), HomeVenueID: bson.NewObjectID(),
			CurrentVenueID: bson.NewObjectID(), Status: models.Available,
			Condition: models.Good, IsActive: true, QrToken: fmt.Sprintf("q-%d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Second), UpdatedAt: base,
		}
		if _, err := conn.DB.Collection("assets").InsertOne(ctx, a); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	svc := service.NewReportService(
		repository.NewAssetRepository(conn.DB), repository.NewVenueRepository(conn.DB),
		repository.NewUserRepository(conn.DB), repository.NewRepairRepository(conn.DB),
		repository.NewDepartmentRepository(conn.DB),
	)
	h := NewReportHandler(svc)
	app := fiber.New() // no auth middleware; these handlers do no auth of their own
	app.Get("/reports/assets-away", h.AssetsAway)

	// Page 1
	resp, err := app.Test(httptest.NewRequest("GET", "/reports/assets-away?limit=5", nil))
	if err != nil {
		t.Fatalf("test p1: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("p1 status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var p1 awayEnvelope
	if err := json.Unmarshal(body, &p1); err != nil {
		t.Fatalf("unmarshal p1 %s: %v", body, err)
	}
	if len(p1.Data) != 5 || p1.Meta.Cursor == nil || !p1.Meta.Cursor.HasMore || p1.Meta.Cursor.NextCursor == "" {
		t.Fatalf("p1 envelope wrong: %+v cursor=%+v", p1.Data, p1.Meta.Cursor)
	}

	// Page 2 via nextCursor
	resp2, _ := app.Test(httptest.NewRequest("GET", "/reports/assets-away?limit=5&cursor="+p1.Meta.Cursor.NextCursor, nil))
	body2, _ := io.ReadAll(resp2.Body)
	var p2 awayEnvelope
	if err := json.Unmarshal(body2, &p2); err != nil {
		t.Fatalf("unmarshal p2: %v", err)
	}
	if len(p2.Data) != 2 || p2.Meta.Cursor.HasMore || p2.Meta.Cursor.NextCursor != "" {
		t.Fatalf("p2 envelope wrong: len=%d cursor=%+v", len(p2.Data), p2.Meta.Cursor)
	}
}

func TestReportHandlerIT_MalformedCursorIs400(t *testing.T) {
	// A bare handler with a nil service is fine: ParseKeyset rejects the cursor
	// before the service is ever called.
	h := NewReportHandler(nil)
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))})
	app.Get("/reports/assets-away", h.AssetsAway)
	resp, err := app.Test(httptest.NewRequest("GET", "/reports/assets-away?cursor=@@bad@@", nil))
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}
