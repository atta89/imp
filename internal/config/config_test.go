package config

import (
	"os"
	"testing"
)

func TestLoad_AttachmentDefaults(t *testing.T) {
	setRequiredEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.StorageBaseDir, "/var/lib/imp/attachments"; got != want {
		t.Errorf("StorageBaseDir = %q, want %q", got, want)
	}
	if got, want := cfg.AttachmentSweepCron, "0 3 * * *"; got != want {
		t.Errorf("AttachmentSweepCron = %q, want %q", got, want)
	}
	if got, want := cfg.AttachmentMaxBytes, int64(10_485_760); got != want {
		t.Errorf("AttachmentMaxBytes = %d, want %d", got, want)
	}
	if got, want := cfg.AttachmentMaxPerRequest, 5; got != want {
		t.Errorf("AttachmentMaxPerRequest = %d, want %d", got, want)
	}
}

func TestLoad_AttachmentOverrides(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("STORAGE_BASE_DIR", "/tmp/atts")
	t.Setenv("ATTACHMENT_SWEEP_CRON", "5 4 * * *")
	t.Setenv("ATTACHMENT_MAX_BYTES", "2048")
	t.Setenv("ATTACHMENT_MAX_PER_REQ", "3")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StorageBaseDir != "/tmp/atts" {
		t.Errorf("StorageBaseDir = %q", cfg.StorageBaseDir)
	}
	if cfg.AttachmentSweepCron != "5 4 * * *" {
		t.Errorf("AttachmentSweepCron = %q", cfg.AttachmentSweepCron)
	}
	if cfg.AttachmentMaxBytes != 2048 {
		t.Errorf("AttachmentMaxBytes = %d", cfg.AttachmentMaxBytes)
	}
	if cfg.AttachmentMaxPerRequest != 3 {
		t.Errorf("AttachmentMaxPerRequest = %d", cfg.AttachmentMaxPerRequest)
	}
}

func TestLoad_AssetIDsDefaultsAndOverrides(t *testing.T) {
	// Required env for Load() to succeed.
	t.Setenv("MONGO_URI", "mongodb://localhost:27017")
	t.Setenv("MONGO_DB", "imp_test")
	t.Setenv("JWT_SECRET", "x")

	// Defaults when unset.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AssetIDsMaxLimit != 100000 {
		t.Fatalf("AssetIDsMaxLimit default = %d, want 100000", cfg.AssetIDsMaxLimit)
	}
	if cfg.AssetIDsBatchSize != 1000 {
		t.Fatalf("AssetIDsBatchSize default = %d, want 1000", cfg.AssetIDsBatchSize)
	}

	// Overrides.
	t.Setenv("ASSET_IDS_MAX_LIMIT", "250")
	t.Setenv("ASSET_IDS_BATCH_SIZE", "50")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AssetIDsMaxLimit != 250 {
		t.Fatalf("AssetIDsMaxLimit = %d, want 250", cfg.AssetIDsMaxLimit)
	}
	if cfg.AssetIDsBatchSize != 50 {
		t.Fatalf("AssetIDsBatchSize = %d, want 50", cfg.AssetIDsBatchSize)
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	// Load() rejects a config missing these — set stubs so we can exercise
	// the attachment defaults.
	t.Setenv("MONGO_URI", "mongodb://localhost")
	t.Setenv("MONGO_DB", "test")
	t.Setenv("JWT_SECRET", "test")
	// Clear anything the parent shell might've set that would fight t.Setenv semantics.
	_ = os.Unsetenv("STORAGE_BASE_DIR")
	_ = os.Unsetenv("ATTACHMENT_SWEEP_CRON")
	_ = os.Unsetenv("ATTACHMENT_MAX_BYTES")
	_ = os.Unsetenv("ATTACHMENT_MAX_PER_REQ")
}
