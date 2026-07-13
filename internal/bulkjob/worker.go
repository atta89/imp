// Package bulkjob runs the in-process worker that drains queued async
// bulk-asset jobs. It mirrors the notification outbox worker's lifecycle
// (context-cancellable goroutines started from main), but the actual job
// execution — atomic claim, batched transactions, cursor/lease bookkeeping —
// lives in service.BulkJobService so it sits next to the shared apply* helpers.
package bulkjob

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Runner is the slice of BulkJobService the worker needs. Kept as an interface
// so the worker can be unit-tested without a live service/Mongo.
type Runner interface {
	// ClaimAndRunOnce claims the oldest runnable job and runs it to a terminal
	// state. Returns claimed=false when nothing was available.
	ClaimAndRunOnce(ctx context.Context, workerID string) (claimed bool, err error)
}

type Config struct {
	Concurrency  int
	PollInterval time.Duration
}

type Worker struct {
	runner       Runner
	logger       *slog.Logger
	concurrency  int
	pollInterval time.Duration
	instanceID   string
}

func NewWorker(runner Runner, logger *slog.Logger, cfg Config) *Worker {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	// A per-process instance id disambiguates lease ownership across replicas
	// and across the worker's own goroutines.
	return &Worker{
		runner:       runner,
		logger:       logger,
		concurrency:  cfg.Concurrency,
		pollInterval: cfg.PollInterval,
		instanceID:   bson.NewObjectID().Hex(),
	}
}

// Run blocks until ctx is cancelled, launching `concurrency` claim/run loops.
// Intended to be started in a goroutine from main and stopped by cancelling the
// shutdown context.
func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("bulk_job_worker_started",
		slog.Int("concurrency", w.concurrency),
		slog.Duration("poll_interval", w.pollInterval),
		slog.String("instance", w.instanceID),
	)
	done := make(chan struct{})
	for i := 0; i < w.concurrency; i++ {
		workerID := fmt.Sprintf("%s-%d", w.instanceID, i)
		go func() {
			defer func() { done <- struct{}{} }()
			w.loop(ctx, workerID)
		}()
	}
	for i := 0; i < w.concurrency; i++ {
		<-done
	}
	w.logger.Info("bulk_job_worker_stopped", slog.String("instance", w.instanceID))
}

// loop drains jobs greedily: as long as ClaimAndRunOnce keeps claiming work it
// runs back-to-back; when the queue is empty it sleeps one poll interval. This
// keeps latency low under load without busy-spinning when idle.
func (w *Worker) loop(ctx context.Context, workerID string) {
	timer := time.NewTimer(w.pollInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		claimed, err := w.runner.ClaimAndRunOnce(ctx, workerID)
		if err != nil {
			w.logger.Error("bulk_job_run_failed", slog.String("worker", workerID), slog.Any("err", err))
		}
		if claimed {
			// Immediately try for the next job.
			continue
		}

		// Nothing to do — wait a poll interval or until shutdown.
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(w.pollInterval)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
