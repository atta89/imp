package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Port                    string
	AppBaseURL              string
	MongoURI                string
	MongoDB                 string
	JWTSecret               string
	JWTAccess               time.Duration
	JWTRefresh              time.Duration
	GmailUser               string
	GmailPass               string
	SMTPHost                string
	SMTPPort                string
	LogLevel                slog.Level
	SeedAdminEmail          string
	SeedAdminPassword       string
	SeedAdminName           string
	OverdueCron             string
	QRLogoPath              string
	FrontendBaseURL         string
	StorageBaseDir          string
	AttachmentSweepCron     string
	AttachmentMaxBytes      int64
	AttachmentMaxPerRequest int

	// Async bulk-asset jobs.
	BulkMaxAssets          int           // max assetIds accepted per bulk request (deduped)
	BulkBatchSize          int           // assets processed per Mongo transaction
	BulkWorkerConcurrency  int           // number of in-process claim/run goroutines
	BulkWorkerPollInterval time.Duration // idle poll cadence when no job is claimable
	BulkJobLease           time.Duration // worker lease duration; reclaimed if it expires
	BulkJobMaxAttempts     int           // per-batch retry cap before rows are errored out
	BulkJobErrorCap        int           // max row errors retained on a job doc
	BulkResultTTL          time.Duration // retention for rendered qr PDFs
	BulkResultCleanupCron  string        // cron for deleting expired job results

	// Async asset-ids export job.
	AssetIDsMaxLimit  int // request cap AND default for POST /assets/bulk/ids limit
	AssetIDsBatchSize int // keyset batch size for the ids scan
}

func Load() (*Config, error) {
	// .env is optional — env vars from the shell win regardless.
	_ = godotenv.Load()

	cfg := &Config{
		Port:       getEnv("PORT", "8080"),
		AppBaseURL: getEnv("APP_BASE_URL", "http://localhost:8080"),
		MongoURI:   os.Getenv("MONGO_URI"),
		MongoDB:    os.Getenv("MONGO_DB"),
		JWTSecret:  os.Getenv("JWT_SECRET"),
		GmailUser:  os.Getenv("GMAIL_USER"),
		GmailPass:  os.Getenv("GMAIL_APP_PASSWORD"),
		SMTPHost:   getEnv("SMTP_HOST", "smtp.gmail.com"),
		SMTPPort:   getEnv("SMTP_PORT", "587"),

		SeedAdminEmail:    os.Getenv("SEED_ADMIN_EMAIL"),
		SeedAdminPassword: os.Getenv("SEED_ADMIN_PASSWORD"),
		SeedAdminName:     getEnv("SEED_ADMIN_NAME", "Admin"),

		OverdueCron: getEnv("OVERDUE_CRON", "0 9 * * *"),

		// Optional override for the QR center logo. If empty, the embedded
		// default logo (internal/qr/assets/logo.png) is used.
		QRLogoPath: os.Getenv("QR_LOGO_PATH"),
	}

	var err error
	cfg.StorageBaseDir = getEnv("STORAGE_BASE_DIR", "/var/lib/imp/attachments")
	cfg.AttachmentSweepCron = getEnv("ATTACHMENT_SWEEP_CRON", "0 3 * * *")

	if cfg.AttachmentMaxBytes, err = parseInt64("ATTACHMENT_MAX_BYTES", "10485760"); err != nil {
		return nil, err
	}
	if cfg.AttachmentMaxPerRequest, err = parseInt("ATTACHMENT_MAX_PER_REQ", "5"); err != nil {
		return nil, err
	}

	if cfg.BulkMaxAssets, err = parseInt("BULK_MAX_ASSETS", "5000"); err != nil {
		return nil, err
	}
	if cfg.BulkBatchSize, err = parseInt("BULK_BATCH_SIZE", "100"); err != nil {
		return nil, err
	}
	if cfg.BulkWorkerConcurrency, err = parseInt("BULK_WORKER_CONCURRENCY", "1"); err != nil {
		return nil, err
	}
	if cfg.BulkWorkerPollInterval, err = parseDuration("BULK_WORKER_POLL_INTERVAL", "5s"); err != nil {
		return nil, err
	}
	var leaseSecs int
	if leaseSecs, err = parseInt("BULK_JOB_LEASE_SECONDS", "120"); err != nil {
		return nil, err
	}
	cfg.BulkJobLease = time.Duration(leaseSecs) * time.Second
	if cfg.BulkJobMaxAttempts, err = parseInt("BULK_JOB_MAX_ATTEMPTS", "5"); err != nil {
		return nil, err
	}
	if cfg.BulkJobErrorCap, err = parseInt("BULK_JOB_ERROR_CAP", "1000"); err != nil {
		return nil, err
	}
	var resultTTLDays int
	if resultTTLDays, err = parseInt("BULK_RESULT_TTL_DAYS", "7"); err != nil {
		return nil, err
	}
	cfg.BulkResultTTL = time.Duration(resultTTLDays) * 24 * time.Hour
	cfg.BulkResultCleanupCron = getEnv("BULK_RESULT_CLEANUP_CRON", "0 4 * * *")

	if cfg.AssetIDsMaxLimit, err = parseInt("ASSET_IDS_MAX_LIMIT", "100000"); err != nil {
		return nil, err
	}
	if cfg.AssetIDsBatchSize, err = parseInt("ASSET_IDS_BATCH_SIZE", "1000"); err != nil {
		return nil, err
	}

	// FrontendBaseURL is the host scanned QR codes and email links resolve
	// to — the public web app, NOT the API. Falls back to APP_BASE_URL so
	// existing dev setups keep working until the frontend gets its own URL.
	cfg.FrontendBaseURL = getEnv("FRONTEND_BASE_URL", cfg.AppBaseURL)
	if cfg.JWTAccess, err = parseDuration("JWT_ACCESS_TTL", "15m"); err != nil {
		return nil, err
	}
	if cfg.JWTRefresh, err = parseDuration("JWT_REFRESH_TTL", "720h"); err != nil {
		return nil, err
	}
	cfg.LogLevel = parseLogLevel(getEnv("LOG_LEVEL", "info"))

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var missing []string
	if c.MongoURI == "" {
		missing = append(missing, "MONGO_URI")
	}
	if c.MongoDB == "" {
		missing = append(missing, "MONGO_DB")
	}
	if c.JWTSecret == "" {
		missing = append(missing, "JWT_SECRET")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(key, fallback string) (time.Duration, error) {
	raw := getEnv(key, fallback)
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %s=%q: %w", key, raw, err)
	}
	return d, nil
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func parseInt64(key, fallback string) (int64, error) {
	raw := getEnv(key, fallback)
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid int64 %s=%q: %w", key, raw, err)
	}
	return n, nil
}

func parseInt(key, fallback string) (int, error) {
	raw := getEnv(key, fallback)
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid int %s=%q: %w", key, raw, err)
	}
	return n, nil
}
