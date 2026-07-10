package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalDisk_PutGetRoundtrip(t *testing.T) {
	root := t.TempDir()
	fs, err := NewLocalDisk(root)
	if err != nil {
		t.Fatal(err)
	}
	key := "aabbccdd11223344aabbccdd11223344" // 32 hex chars
	payload := []byte("hello attachments")

	if err := fs.Put(context.Background(), key, bytes.NewReader(payload), "text/plain", int64(len(payload))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := fs.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: %q", got)
	}

	// Verify sharded layout: <root>/aa/bb/<key>
	want := filepath.Join(root, "aa", "bb", key)
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected file at %s: %v", want, err)
	}
}

func TestLocalDisk_DeleteIdempotent(t *testing.T) {
	root := t.TempDir()
	fs, _ := NewLocalDisk(root)
	key := "1122334455667788aabbccddeeff0011"
	// Delete without Put must not error.
	if err := fs.Delete(context.Background(), key); err != nil {
		t.Errorf("Delete on missing key: %v", err)
	}
	// Delete after Put must succeed, second delete must be idempotent.
	_ = fs.Put(context.Background(), key, strings.NewReader("x"), "text/plain", 1)
	if err := fs.Delete(context.Background(), key); err != nil {
		t.Errorf("Delete after Put: %v", err)
	}
	if err := fs.Delete(context.Background(), key); err != nil {
		t.Errorf("Delete second time: %v", err)
	}
}

func TestLocalDisk_GetMissingReturnsError(t *testing.T) {
	fs, _ := NewLocalDisk(t.TempDir())
	if _, err := fs.Get(context.Background(), "00000000000000000000000000000000"); err == nil {
		t.Error("expected error on Get missing")
	}
}

func TestLocalDisk_RejectsShortKey(t *testing.T) {
	fs, _ := NewLocalDisk(t.TempDir())
	if err := fs.Put(context.Background(), "short", strings.NewReader("x"), "text/plain", 1); err == nil {
		t.Error("expected error on short key")
	}
}

func TestNewKey_ReturnsHex32(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		k, err := NewKey()
		if err != nil {
			t.Fatal(err)
		}
		if len(k) != 32 {
			t.Errorf("len(NewKey) = %d, want 32", len(k))
		}
		for _, c := range k {
			if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
				t.Errorf("non-hex char in key: %q", k)
				break
			}
		}
		if seen[k] {
			t.Errorf("duplicate key returned")
		}
		seen[k] = true
	}
}
