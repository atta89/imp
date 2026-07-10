// Package storage abstracts blob storage for attachments. The MVP
// implementation is LocalDisk; an S3-compatible impl can be added later
// without touching call sites.
package storage

import (
	"context"
	"io"
)

// FileStorage is the byte-storage seam used by the attachment service.
// Implementations are expected to be safe for concurrent use.
type FileStorage interface {
	Put(ctx context.Context, key string, r io.Reader, contentType string, size int64) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}
