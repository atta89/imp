package bulkjob

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRunner struct {
	mu      sync.Mutex
	jobs    int          // remaining jobs to hand out
	claimed atomic.Int64 // total ClaimAndRunOnce calls that returned claimed=true
	workers map[string]struct{}
}

func (f *fakeRunner) ClaimAndRunOnce(ctx context.Context, workerID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.workers == nil {
		f.workers = map[string]struct{}{}
	}
	f.workers[workerID] = struct{}{}
	if f.jobs <= 0 {
		return false, nil
	}
	f.jobs--
	f.claimed.Add(1)
	return true, nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestWorkerDrainsAllJobsThenIdles(t *testing.T) {
	fr := &fakeRunner{jobs: 25}
	w := NewWorker(fr, quietLogger(), Config{Concurrency: 3, PollInterval: 10 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	// Wait until all jobs are claimed (greedy drain, no waiting a full poll each).
	deadline := time.After(2 * time.Second)
	for fr.claimed.Load() < 25 {
		select {
		case <-deadline:
			t.Fatalf("only %d/25 jobs claimed", fr.claimed.Load())
		case <-time.After(2 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after context cancel")
	}

	if got := fr.claimed.Load(); got != 25 {
		t.Fatalf("claimed = %d, want exactly 25 (no double-run)", got)
	}
}

func TestWorkerUsesDistinctWorkerIDsPerGoroutine(t *testing.T) {
	fr := &fakeRunner{jobs: 0}
	w := NewWorker(fr, quietLogger(), Config{Concurrency: 4, PollInterval: 5 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	fr.mu.Lock()
	n := len(fr.workers)
	fr.mu.Unlock()
	if n != 4 {
		t.Fatalf("distinct worker ids = %d, want 4 (one lease identity per goroutine)", n)
	}
}
