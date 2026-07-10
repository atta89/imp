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
	cron            *cron.Cron
	overdue         *service.OverdueService
	attachments     *service.AttachmentService
	logger          *slog.Logger
	overdueCron     string
	attachmentCron  string
	jobTimeout      time.Duration
}

func New(overdue *service.OverdueService, attachments *service.AttachmentService, logger *slog.Logger, overdueCron, attachmentCron string) *Scheduler {
	return &Scheduler{
		cron:            cron.New(),
		overdue:         overdue,
		attachments:     attachments,
		logger:          logger,
		overdueCron:     overdueCron,
		attachmentCron:  attachmentCron,
		jobTimeout:      5 * time.Minute,
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
	s.cron.Start()
	s.logger.Info("scheduler_started",
		slog.String("overdue_cron", s.overdueCron),
		slog.String("attachment_cron", s.attachmentCron),
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
