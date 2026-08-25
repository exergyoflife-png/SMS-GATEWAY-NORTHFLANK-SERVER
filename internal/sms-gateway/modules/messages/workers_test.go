package messages //nolint:testpackage // The queue retry behavior is implemented by the private hashing worker.

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestHashingWorkerProcessRemovesSuccessfulBatch(t *testing.T) {
	t.Parallel()

	var processed []uint64
	worker := newHashingWorkerForTest(func(_ context.Context, ids []uint64) (int64, error) {
		processed = append(processed, ids...)
		return int64(len(ids)), nil
	})
	worker.Enqueue(1)
	worker.Enqueue(2)

	worker.process(context.Background())

	assertHashingQueue(t, worker)
	assertIDs(t, processed, 1, 2)
}

func TestHashingWorkerProcessRequeuesFailedBatch(t *testing.T) {
	t.Parallel()

	attempts := 0
	worker := newHashingWorkerForTest(func(_ context.Context, ids []uint64) (int64, error) {
		attempts++
		if attempts == 1 {
			return 0, errors.New("temporary database failure")
		}
		return int64(len(ids)), nil
	})
	worker.Enqueue(1)
	worker.Enqueue(2)

	worker.process(context.Background())
	assertHashingQueue(t, worker, 1, 2)

	worker.process(context.Background())
	assertHashingQueue(t, worker)
	if attempts != 2 {
		t.Fatalf("expected 2 hashing attempts, got %d", attempts)
	}
}

func TestHashingWorkerProcessPreservesConcurrentEnqueueOnFailure(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { closeSignal(release) })

	var batches [][]uint64
	worker := newHashingWorkerForTest(func(_ context.Context, ids []uint64) (int64, error) {
		batches = append(batches, append([]uint64(nil), ids...))
		if len(batches) == 1 {
			close(started)
			<-release

			return 0, errors.New("temporary database failure")
		}

		return int64(len(ids)), nil
	})
	worker.Enqueue(1)

	processDone := make(chan struct{})
	go func() {
		worker.process(context.Background())
		close(processDone)
	}()
	waitForTestResult(t, started, "hashing batch to start")

	enqueueDone := make(chan struct{})
	go func() {
		worker.Enqueue(2)
		close(enqueueDone)
	}()
	waitForTestResult(t, enqueueDone, "concurrent enqueue to finish")

	closeSignal(release)
	waitForTestResult(t, processDone, "failed hashing batch to finish")

	assertHashingQueue(t, worker, 1, 2)

	worker.process(context.Background())

	assertHashingQueue(t, worker)
	if len(batches) != 2 {
		t.Fatalf("expected 2 hashing batches, got %d", len(batches))
	}
	assertIDs(t, batches[0], 1)
	assertIDs(t, batches[1], 1, 2)
}

func newHashingWorkerForTest(hashProcessed func(context.Context, []uint64) (int64, error)) *hashingWorker {
	return &hashingWorker{
		hashProcessed: hashProcessed,
		logger:        zap.NewNop(),
		queue:         make(map[uint64]struct{}),
	}
}

func assertHashingQueue(t *testing.T, worker *hashingWorker, expected ...uint64) {
	t.Helper()

	worker.mux.Lock()
	defer worker.mux.Unlock()

	if len(worker.queue) != len(expected) {
		t.Fatalf("expected %d queued IDs, got %d", len(expected), len(worker.queue))
	}
	for _, id := range expected {
		if _, ok := worker.queue[id]; !ok {
			t.Errorf("expected message ID %d to be queued", id)
		}
	}
}

func assertIDs(t *testing.T, actual []uint64, expected ...uint64) {
	t.Helper()

	if len(actual) != len(expected) {
		t.Fatalf("expected %d IDs, got %d", len(expected), len(actual))
	}
	for _, id := range expected {
		found := false
		for _, candidate := range actual {
			if candidate == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected processed IDs to contain %d", id)
		}
	}
}

func waitForTestResult[T any](t *testing.T, results <-chan T, operation string) T {
	t.Helper()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	select {
	case result := <-results:
		return result
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", operation)
		var zero T
		return zero
	}
}

func closeSignal(signal chan struct{}) {
	select {
	case <-signal:
	default:
		close(signal)
	}
}
