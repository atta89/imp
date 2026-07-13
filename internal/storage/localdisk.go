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
	"strings"
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

// NewKeyWithPrefix returns a fresh key namespaced under a slash-delimited
// prefix, e.g. "bulk-jobs/<32hex>". The prefix organizes stored files by
// producer; the leaf remains a sharded 32-hex key.
func NewKeyWithPrefix(prefix string) (string, error) {
	k, err := NewKey()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(prefix, "/") + "/" + k, nil
}

// pathFor maps a storage key to an on-disk path. A key is either a bare
// 32-hex-char leaf, or "<prefix>/<32hex>" where prefix is one or more safe
// path segments (no "..", not absolute). The leaf is always sharded into
// <root>/<prefix?>/<leaf[0:2]>/<leaf[2:4]>/<leaf>.
func (l *LocalDisk) pathFor(key string) (string, error) {
	prefix, leaf := "", key
	if i := strings.LastIndex(key, "/"); i >= 0 {
		prefix, leaf = key[:i], key[i+1:]
		if prefix == "" || filepath.IsAbs(prefix) || strings.Contains(prefix, "..") {
			return "", fmt.Errorf("invalid storage key prefix: %q", key)
		}
	}
	if len(leaf) != 32 || !isHex32(leaf) {
		return "", fmt.Errorf("storage key must be 32 hex chars (with optional prefix): %q", key)
	}
	return filepath.Join(l.root, filepath.FromSlash(prefix), leaf[0:2], leaf[2:4], leaf), nil
}

func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
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
