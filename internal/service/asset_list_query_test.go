package service

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/models"
)

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }

func TestBuildAssetListQuery_ParsesAllFields(t *testing.T) {
	venue := bson.NewObjectID()
	cur := bson.NewObjectID()
	cat := bson.NewObjectID()
	dept := bson.NewObjectID()
	resp := bson.NewObjectID()

	f := &models.AssetListFilters{
		Venue:        strp(venue.Hex()),
		CurrentVenue: strp(cur.Hex()),
		Category:     strp(cat.Hex()),
		Department:   strp(dept.Hex()),
		Responsible:  strp(resp.Hex()),
		Away:         boolp(true),
		Overdue:      boolp(true),
		Q:            strp("  drill  "),
	}
	st := models.InUse
	f.Status = &st

	q, err := BuildAssetListQuery(f)
	if err != nil {
		t.Fatalf("BuildAssetListQuery: %v", err)
	}
	if q.Venue == nil || *q.Venue != venue {
		t.Errorf("venue mismatch")
	}
	if q.CurrentVenue == nil || *q.CurrentVenue != cur {
		t.Errorf("currentVenue mismatch")
	}
	if q.Category == nil || *q.Category != cat {
		t.Errorf("category mismatch")
	}
	if q.Department == nil || *q.Department != dept {
		t.Errorf("department mismatch")
	}
	if q.Responsible == nil || *q.Responsible != resp {
		t.Errorf("responsible mismatch")
	}
	if q.Status != models.InUse {
		t.Errorf("status = %q", q.Status)
	}
	if !q.Away || !q.Overdue {
		t.Errorf("away/overdue not set")
	}
	if q.Q != "drill" {
		t.Errorf("q = %q, want trimmed 'drill'", q.Q)
	}
	if q.Scope != nil {
		t.Errorf("Scope must be left nil by the builder")
	}
}

func TestBuildAssetListQuery_NilAndEmpty(t *testing.T) {
	q, err := BuildAssetListQuery(nil)
	if err != nil {
		t.Fatalf("nil filters: %v", err)
	}
	if q.Venue != nil || q.Status != "" || q.Away || q.Q != "" {
		t.Errorf("nil filters should yield zero query")
	}
	q, err = BuildAssetListQuery(&models.AssetListFilters{})
	if err != nil {
		t.Fatalf("empty filters: %v", err)
	}
	if q.Venue != nil || q.Away {
		t.Errorf("empty filters should yield zero query")
	}
}

func TestBuildAssetListQuery_MalformedObjectIDs(t *testing.T) {
	cases := []struct {
		name string
		f    *models.AssetListFilters
		want string
	}{
		{"venue", &models.AssetListFilters{Venue: strp("nothex")}, "invalid venue id"},
		{"currentVenue", &models.AssetListFilters{CurrentVenue: strp("nothex")}, "invalid currentVenue id"},
		{"category", &models.AssetListFilters{Category: strp("nothex")}, "invalid category id"},
		{"department", &models.AssetListFilters{Department: strp("nothex")}, "invalid department id"},
		{"responsible", &models.AssetListFilters{Responsible: strp("nothex")}, "invalid responsible id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildAssetListQuery(tc.f)
			appErr, ok := apperror.As(err)
			if !ok || appErr.Kind != apperror.KindBadRequest || appErr.Message != tc.want {
				t.Fatalf("err = %v, want BadRequest %q", err, tc.want)
			}
		})
	}
}
