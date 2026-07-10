package notification

import (
	"context"
	"log/slog"
	"time"

	"imp/internal/models"
)

// Worker drains the outbox: every `interval`, fetches up to `batchSize` queued
// notifications and tries to send each. Failures bump `attempts`; once
// `maxAttempts` is reached, the notification is marked `failed` and skipped
// in future ticks.
type Worker struct {
	repo        *Repository
	mailer      Mailer
	logger      *slog.Logger
	interval    time.Duration
	batchSize   int
	maxAttempts int
}

type WorkerConfig struct {
	Interval    time.Duration
	BatchSize   int
	MaxAttempts int
}

func NewWorker(repo *Repository, mailer Mailer, logger *slog.Logger, cfg WorkerConfig) *Worker {
	if cfg.Interval == 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 50
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 5
	}
	return &Worker{
		repo:        repo,
		mailer:      mailer,
		logger:      logger,
		interval:    cfg.Interval,
		batchSize:   cfg.BatchSize,
		maxAttempts: cfg.MaxAttempts,
	}
}

// Run blocks until ctx is cancelled. Intended to be launched in a goroutine
// from main and stopped by cancelling the shutdown context.
func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("notification_worker_started",
		slog.Duration("interval", w.interval),
		slog.Int("batch_size", w.batchSize),
	)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("notification_worker_stopped")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	notifs, err := w.repo.FindQueued(ctx, w.batchSize)
	if err != nil {
		w.logger.Error("outbox_fetch_failed", slog.Any("err", err))
		return
	}
	for i := range notifs {
		w.sendOne(ctx, &notifs[i])
	}
}

func (w *Worker) sendOne(ctx context.Context, n *models.Notification) {
	err := w.mailer.Send(ctx, Message{
		To:      string(n.RecipientEmail),
		Subject: n.Subject,
		Body:    n.Body,
	})
	if err == nil {
		if uerr := w.repo.MarkSent(ctx, n.ID, time.Now().UTC()); uerr != nil {
			w.logger.Error("mark_sent_failed", slog.Any("err", uerr), slog.String("id", n.ID.Hex()))
		}
		return
	}

	attempts := n.Attempts + 1
	terminal := attempts >= w.maxAttempts
	if uerr := w.repo.RecordFailure(ctx, n.ID, attempts, err.Error(), terminal); uerr != nil {
		w.logger.Error("record_failure_failed", slog.Any("err", uerr), slog.String("id", n.ID.Hex()))
	}
	w.logger.Warn("email_send_failed",
		slog.String("id", n.ID.Hex()),
		slog.Int("attempts", attempts),
		slog.Bool("terminal", terminal),
		slog.Any("err", err),
	)
}
