// Package scheduler hosts the cron jobs that run inside the API process.
// For MVP this is just the daily overdue scan; jobs that get heavy or need
// distributed scheduling later can move to a dedicated worker process.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"imp/internal/service"
)

type Scheduler struct {
	cron           *cron.Cron
	overdue        *service.OverdueService
	attachments    *service.AttachmentService
	bulkJobs       *service.BulkJobService
	logger         *slog.Logger
	overdueCron    string
	attachmentCron string
	bulkResultCron string
	jobTimeout     time.Duration
}

func New(overdue *service.OverdueService, attachments *service.AttachmentService, bulkJobs *service.BulkJobService, logger *slog.Logger, overdueCron, attachmentCron, bulkResultCron string) *Scheduler {
	return &Scheduler{
		cron:           cron.New(),
		overdue:        overdue,
		attachments:    attachments,
		bulkJobs:       bulkJobs,
		logger:         logger,
		overdueCron:    overdueCron,
		attachmentCron: attachmentCron,
		bulkResultCron: bulkResultCron,
		jobTimeout:     5 * time.Minute,
	}
}

// Start registers jobs and kicks off the cron loop. Returns an error if the
// schedule expression doesn't parse.
func (s *Scheduler) Start() error {
	if _, err := s.cron.AddFunc(s.overdueCron, s.runOverdueScan); err != nil {
		return fmt.Errorf("register overdue job (%q): %w", s.overdueCron, err)
	}
	if _, err := s.cron.AddFunc(s.attachmentCron, s.runAttachmentSweep); err != nil {
		return fmt.Errorf("register attachment sweep (%q): %w", s.attachmentCron, err)
	}
	if s.bulkJobs != nil && s.bulkResultCron != "" {
		if _, err := s.cron.AddFunc(s.bulkResultCron, s.runBulkResultCleanup); err != nil {
			return fmt.Errorf("register bulk result cleanup (%q): %w", s.bulkResultCron, err)
		}
	}
	s.cron.Start()
	s.logger.Info("scheduler_started",
		slog.String("overdue_cron", s.overdueCron),
		slog.String("attachment_cron", s.attachmentCron),
		slog.String("bulk_result_cron", s.bulkResultCron),
	)
	return nil
}

// Stop signals the cron to stop accepting new jobs and waits for any
// currently-running job to finish (bounded by the caller's ctx).
func (s *Scheduler) Stop(ctx context.Context) {
	doneCtx := s.cron.Stop()
	select {
	case <-doneCtx.Done():
		s.logger.Info("scheduler_stopped_cleanly")
	case <-ctx.Done():
		s.logger.Warn("scheduler_stop_timeout")
	}
}

func (s *Scheduler) runOverdueScan() {
	ctx, cancel := context.WithTimeout(context.Background(), s.jobTimeout)
	defer cancel()
	if _, err := s.overdue.RunDailyScan(ctx); err != nil {
		s.logger.Error("overdue_scan_failed", slog.Any("err", err))
	}
}

func (s *Scheduler) runAttachmentSweep() {
	ctx, cancel := context.WithTimeout(context.Background(), s.jobTimeout)
	defer cancel()
	if err := s.attachments.OrphanSweep(ctx); err != nil {
		s.logger.Error("attachment_sweep_failed", slog.Any("err", err))
	}
}

func (s *Scheduler) runBulkResultCleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), s.jobTimeout)
	defer cancel()
	n, err := s.bulkJobs.CleanupExpiredResults(ctx)
	if err != nil {
		s.logger.Error("bulk_result_cleanup_failed", slog.Any("err", err))
		return
	}
	if n > 0 {
		s.logger.Info("bulk_result_cleanup", slog.Int("cleaned", n))
	}
}
