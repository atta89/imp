package pagination

import (
	"encoding/base64"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	// Millisecond precision mirrors what MongoDB stores/returns.
	ts := time.Date(2026, 3, 4, 5, 6, 7, 890*int(time.Millisecond), time.UTC)
	id := bson.NewObjectID()
	in := Cursor{CreatedAt: ts, ID: id}

	got, err := Decode(Encode(in))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got == nil {
		t.Fatal("Decode returned nil cursor")
	}
	if !got.CreatedAt.Equal(in.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, in.CreatedAt)
	}
	if got.ID != in.ID {
		t.Errorf("ID = %v, want %v", got.ID, in.ID)
	}
}

func TestDecodeEmptyIsFirstPage(t *testing.T) {
	got, err := Decode("")
	if err != nil {
		t.Fatalf("Decode(\"\"): %v", err)
	}
	if got != nil {
		t.Errorf("Decode(\"\") = %v, want nil", got)
	}
}

func TestDecodeMalformedIsBadRequest(t *testing.T) {
	for _, raw := range []string{
		"not-base64!!!",
		"",
		"x",
		// Valid base64url that decodes to a payload missing the "|" separator.
		base64.RawURLEncoding.EncodeToString([]byte("abc")),
		// Valid base64url that decodes to a non-numeric timestamp.
		base64.RawURLEncoding.EncodeToString([]byte("notint|64d2f8a1c3b2a10012345678")),
		// Valid base64url that decodes to a bad hex ObjectID.
		base64.RawURLEncoding.EncodeToString([]byte("123|xyz")),
	} {
		if raw == "" {
			continue // empty is the first-page sentinel, covered above
		}
		_, err := Decode(raw)
		if err == nil {
			t.Errorf("Decode(%q) err = nil, want BadRequest", raw)
			continue
		}
		if ae, ok := err.(*apperror.Error); !ok || ae.Kind != apperror.KindBadRequest {
			t.Errorf("Decode(%q) err = %v, want *apperror.Error KindBadRequest", raw, err)
		}
	}
}
