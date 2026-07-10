package repository

import "testing"

// Placeholder; real Mongo-backed coverage lives in service tests.
// This file exists so `go test ./internal/repository/...` succeeds and
// future pure helpers have a home.
func TestAttachmentRepository_Compile(t *testing.T) {
	var _ = NewAttachmentRepository
}
