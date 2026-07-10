package service

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
)

// stubFS is a minimal FileStorage that captures the last Put payload in memory.
type stubFS struct {
	putCalls    int
	deleteCalls int
	lastKey     string
	lastBytes   []byte
	putErr      error
}

func (s *stubFS) Put(_ context.Context, key string, r io.Reader, _ string, _ int64) error {
	s.putCalls++
	s.lastKey = key
	b, _ := io.ReadAll(r)
	s.lastBytes = b
	return s.putErr
}
func (s *stubFS) Get(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (s *stubFS) Delete(_ context.Context, _ string) error           { s.deleteCalls++; return nil }

// stubAttachmentInserter captures Insert calls without hitting Mongo, and
// mimics the real repository's ID-generation behavior.
type stubAttachmentInserter struct {
	inserted []*models.Attachment
}

func newStubAttachmentInserter() *stubAttachmentInserter {
	return &stubAttachmentInserter{}
}

func (s *stubAttachmentInserter) Insert(_ context.Context, a *models.Attachment) error {
	// Simulate ID generation.
	if a.ID.IsZero() {
		a.ID = bson.NewObjectID()
	}
	s.inserted = append(s.inserted, a)
	return nil
}

// newTestAttachmentService wires a service against stub deps. Callers pass a
// custom AttachmentConfig or nil for defaults (10 MB, 5 per request).
func newTestAttachmentService(cfg *AttachmentConfig) (*AttachmentService, *stubFS) {
	fs := &stubFS{}
	c := AttachmentConfig{MaxBytes: 10 * 1024 * 1024, MaxPerRequest: 5}
	if cfg != nil {
		c = *cfg
	}
	svc := &AttachmentService{
		attachments: newStubAttachmentInserter(),
		storage:     fs,
		cfg:         c,
	}
	return svc, fs
}

func TestUpload_RejectsEmpty(t *testing.T) {
	svc, _ := newTestAttachmentService(nil)
	_, err := svc.Upload(context.Background(), bson.NewObjectID(), "x.png", strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error on empty upload")
	}
}

func TestUpload_RejectsUnknownContentType(t *testing.T) {
	svc, _ := newTestAttachmentService(nil)
	// A short ASCII payload sniffs as text/plain, not in the allowed set.
	_, err := svc.Upload(context.Background(), bson.NewObjectID(), "note.txt", strings.NewReader("just some text"))
	if err == nil {
		t.Fatal("expected error on unknown content type")
	}
}

func TestUpload_RejectsOversize(t *testing.T) {
	svc, _ := newTestAttachmentService(&AttachmentConfig{MaxBytes: 10, MaxPerRequest: 5})
	// PNG header prefix so sniff picks image/png; exceed 10-byte cap.
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{'a'}, 100)...)
	_, err := svc.Upload(context.Background(), bson.NewObjectID(), "big.png", bytes.NewReader(png))
	if err == nil {
		t.Fatal("expected error on oversize upload")
	}
}

func TestUpload_HappyPath_PNG(t *testing.T) {
	svc, fs := newTestAttachmentService(nil)
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{'x'}, 100)...)
	att, err := svc.Upload(context.Background(), bson.NewObjectID(), "shot.png", bytes.NewReader(png))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if att.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want image/png", att.ContentType)
	}
	if att.Filename != "shot.png" {
		t.Errorf("Filename = %q", att.Filename)
	}
	if att.Linked {
		t.Error("Linked should be false on fresh upload")
	}
	if fs.putCalls != 1 {
		t.Errorf("Put called %d times, want 1", fs.putCalls)
	}
	if !bytes.Equal(fs.lastBytes, png) {
		t.Error("bytes on storage do not match input")
	}
}

func TestUpload_IgnoresClientContentType(t *testing.T) {
	// If the sniff succeeds, the client's filename extension is irrelevant.
	svc, _ := newTestAttachmentService(nil)
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{'y'}, 50)...)
	att, err := svc.Upload(context.Background(), bson.NewObjectID(), "actually-a-png.pdf", bytes.NewReader(png))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if att.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want image/png", att.ContentType)
	}
}

// stubAttachmentReader is a Find-by-IDs stub that keeps the attachment
// service testable without live Mongo.
type stubAttachmentReader struct {
	store map[bson.ObjectID]*models.Attachment
}

func (s *stubAttachmentReader) FindByIDs(_ context.Context, ids []bson.ObjectID) ([]models.Attachment, error) {
	var out []models.Attachment
	for _, id := range ids {
		if a, ok := s.store[id]; ok {
			out = append(out, *a)
		}
	}
	return out, nil
}

func newTestAttachmentServiceWithStore(store map[bson.ObjectID]*models.Attachment) *AttachmentService {
	return &AttachmentService{
		attachments: newStubAttachmentInserter(),
		reader:      &stubAttachmentReader{store: store},
		storage:     &stubFS{},
		cfg:         AttachmentConfig{MaxBytes: 10 * 1024 * 1024, MaxPerRequest: 5},
	}
}

func TestValidate_EmptyIsOK(t *testing.T) {
	svc, _ := newTestAttachmentService(nil)
	got, err := svc.Validate(context.Background(), nil, bson.NewObjectID())
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK {
		t.Error("empty ids should be OK")
	}
}

func TestValidate_TooMany_ReturnsGateError(t *testing.T) {
	svc, _ := newTestAttachmentService(&AttachmentConfig{MaxBytes: 1, MaxPerRequest: 2})
	ids := []bson.ObjectID{bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()}
	got, err := svc.Validate(context.Background(), ids, bson.NewObjectID())
	if err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Fatal("expected not OK")
	}
	if got.GateError == nil {
		t.Fatal("expected GateError")
	}
	if len(got.PerAttachment) != 0 {
		t.Errorf("expected no per-attachment entries on gate failure, got %d", len(got.PerAttachment))
	}
}

func TestValidate_Duplicate_ReturnsGateError(t *testing.T) {
	svc, _ := newTestAttachmentService(nil)
	id := bson.NewObjectID()
	got, err := svc.Validate(context.Background(), []bson.ObjectID{id, id}, bson.NewObjectID())
	if err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Fatal("expected not OK on duplicate")
	}
	if got.GateError == nil {
		t.Fatal("expected GateError")
	}
}

func TestValidate_PerAttachmentErrors_PreservesOrder(t *testing.T) {
	// Seed the repo with three attachments — one owned & unlinked, one
	// foreign-owner, one linked.
	user := bson.NewObjectID()
	other := bson.NewObjectID()
	okAtt := &models.Attachment{ID: bson.NewObjectID(), UploadedBy: user, Linked: false}
	foreign := &models.Attachment{ID: bson.NewObjectID(), UploadedBy: other, Linked: false}
	linked := &models.Attachment{ID: bson.NewObjectID(), UploadedBy: user, Linked: true}
	missing := bson.NewObjectID()

	svc := newTestAttachmentServiceWithStore(map[bson.ObjectID]*models.Attachment{
		okAtt.ID:   okAtt,
		foreign.ID: foreign,
		linked.ID:  linked,
	})

	ids := []bson.ObjectID{okAtt.ID, missing, foreign.ID, linked.ID}
	got, err := svc.Validate(context.Background(), ids, user)
	if err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Fatal("expected not OK — three of four fail")
	}
	if len(got.PerAttachment) != 4 {
		t.Fatalf("expected 4 per-attachment entries, got %d", len(got.PerAttachment))
	}
	if got.PerAttachment[0].AttachmentId != okAtt.ID || !got.PerAttachment[0].Ok {
		t.Errorf("entry 0: %+v", got.PerAttachment[0])
	}
	if got.PerAttachment[1].AttachmentId != missing || got.PerAttachment[1].Ok || got.PerAttachment[1].Error == nil || *got.PerAttachment[1].Error != models.AttachmentValidationErrorErrorNotFound {
		t.Errorf("entry 1: %+v", got.PerAttachment[1])
	}
	if got.PerAttachment[2].AttachmentId != foreign.ID || got.PerAttachment[2].Ok || got.PerAttachment[2].Error == nil || *got.PerAttachment[2].Error != models.AttachmentValidationErrorErrorNotOwner {
		t.Errorf("entry 2: %+v", got.PerAttachment[2])
	}
	if got.PerAttachment[3].AttachmentId != linked.ID || got.PerAttachment[3].Ok || got.PerAttachment[3].Error == nil || *got.PerAttachment[3].Error != models.AttachmentValidationErrorErrorAlreadyLinked {
		t.Errorf("entry 3: %+v", got.PerAttachment[3])
	}
}

func TestValidate_AllOK(t *testing.T) {
	user := bson.NewObjectID()
	a := &models.Attachment{ID: bson.NewObjectID(), UploadedBy: user, Linked: false}
	b := &models.Attachment{ID: bson.NewObjectID(), UploadedBy: user, Linked: false}
	svc := newTestAttachmentServiceWithStore(map[bson.ObjectID]*models.Attachment{a.ID: a, b.ID: b})
	got, err := svc.Validate(context.Background(), []bson.ObjectID{a.ID, b.ID}, user)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK {
		t.Fatalf("expected OK, got %+v", got)
	}
}

func TestLink_EmptyIDsIsNoOp(t *testing.T) {
	svc := &AttachmentService{repo: nil} // no repo needed for empty case
	if err := svc.Link(context.Background(), nil, bson.NewObjectID(), bson.NewObjectID()); err != nil {
		t.Errorf("Link(nil) error: %v", err)
	}
}

// Real Link + OrphanSweep coverage against live Mongo goes in the service
// integration tests (see internal/service/*_integration_test.go if the
// project uses testcontainers). Below is a compile-time check that the
// method exists with the right signature.
func TestOrphanSweep_SignatureCompiles(t *testing.T) {
	var _ func(context.Context) error = (&AttachmentService{}).OrphanSweep
}
