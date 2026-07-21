package response

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestPaginatedCursorEnvelope(t *testing.T) {
	app := fiber.New()
	app.Get("/x", func(c *fiber.Ctx) error {
		return PaginatedCursor(c, []string{"a", "b"}, CursorPage{
			Limit: 25, HasMore: true, NextCursor: "TOKEN",
		})
	})

	resp, err := app.Test(httptest.NewRequest("GET", "/x", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)

	var env struct {
		Data []string `json:"data"`
		Meta struct {
			Pagination *Pagination `json:"pagination"`
			Cursor     *CursorPage `json:"cursor"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if env.Meta.Pagination != nil {
		t.Errorf("meta.pagination should be omitted, got %+v", env.Meta.Pagination)
	}
	if env.Meta.Cursor == nil {
		t.Fatal("meta.cursor missing")
	}
	if env.Meta.Cursor.Limit != 25 || !env.Meta.Cursor.HasMore || env.Meta.Cursor.NextCursor != "TOKEN" {
		t.Errorf("cursor = %+v", env.Meta.Cursor)
	}
	if len(env.Data) != 2 {
		t.Errorf("data len = %d, want 2", len(env.Data))
	}
}

func TestCursorNextCursorOmittedWhenEmpty(t *testing.T) {
	app := fiber.New()
	app.Get("/x", func(c *fiber.Ctx) error {
		return PaginatedCursor(c, []string{}, CursorPage{Limit: 25, HasMore: false})
	})
	resp, _ := app.Test(httptest.NewRequest("GET", "/x", nil))
	body, _ := io.ReadAll(resp.Body)
	if got := string(body); jsonHasKey(got, "nextCursor") {
		t.Errorf("nextCursor should be omitted: %s", got)
	}
}

func jsonHasKey(s, key string) bool {
	return json.Valid([]byte(s)) && strings.Contains(s, "\""+key+"\"")
}
