package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// LocalDisk stores files under a root directory, sharded by the first four
// hex characters of the key into <root>/<key[0:2]>/<key[2:4]>/<key>.
type LocalDisk struct {
	root string
}

// NewLocalDisk verifies the root exists (or creates it) and returns a
// LocalDisk. It does not chown or chmod — that's an ops concern.
func NewLocalDisk(root string) (*LocalDisk, error) {
	if root == "" {
		return nil, errors.New("storage root is empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir storage root %q: %w", root, err)
	}
	return &LocalDisk{root: root}, nil
}

// NewKey returns a fresh 32-hex-char storage key backed by 16 random bytes.
// The key is opaque and unrelated to any filename or user ID.
func NewKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (l *LocalDisk) pathFor(key string) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("storage key must be 32 hex chars: %q", key)
	}
	return filepath.Join(l.root, key[0:2], key[2:4], key), nil
}

// Put streams r to <path>.tmp then atomically renames to <path>. If the
// underlying reader errors mid-copy, the partial .tmp file is removed.
func (l *LocalDisk) Put(ctx context.Context, key string, r io.Reader, contentType string, size int64) error {
	_ = ctx // reserved for cancellation in future impls
	_ = contentType
	_ = size
	full, err := l.pathFor(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("mkdir shard: %w", err)
	}
	tmp := full + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tmp: %w", err)
	}
	return nil
}

func (l *LocalDisk) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	_ = ctx
	full, err := l.pathFor(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Delete removes the file. A missing file is not an error (idempotent).
func (l *LocalDisk) Delete(ctx context.Context, key string) error {
	_ = ctx
	full, err := l.pathFor(key)
	if err != nil {
		return err
	}
	err = os.Remove(full)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
