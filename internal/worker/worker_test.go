package worker

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// logCapture is a slog.Handler that records messages so a test can assert an
// slog.Error / slog.Warn record was emitted on a persistence-failure path.
type logCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.msgs = append(c.msgs, r.Message)
	c.mu.Unlock()
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

func (c *logCapture) contains(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

func (c *logCapture) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// waitForTerminal drains ch until it sees a terminal event for jobID.
func waitForTerminal(t *testing.T, ch <-chan Event, jobID string) Event {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.JobID != jobID {
				continue
			}
			switch e.Type {
			case EventDone, EventFailed, EventCanceled:
				return e
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a terminal event for job %s", jobID)
		}
	}
}

const blockJobFinishTrigger = `CREATE TRIGGER abr_block_job_finish BEFORE UPDATE ON jobs
	 WHEN NEW.finished_at IS NOT NULL
	 BEGIN SELECT RAISE(ABORT, 'job finish write blocked for test'); END`

// When the terminal FinishJob write fails, the worker must not publish a clean
// "done" event the database never recorded: it logs the failure at Error and
// still emits the terminal event, but with Error set so the client learns the
// outcome is unpersisted.
func TestFinish_HandlerDoneButPersistFails(t *testing.T) {
	d := testDB(t)
	logs := captureLogs(t)
	m := New(d, 1)
	defer m.Shutdown()
	m.Register(model.JobScan, func(ctx context.Context, j model.Job, p *Progress) error { return nil })

	ch, unsub, _ := m.Subscribe("", 0)
	defer unsub()

	if _, err := d.Exec(blockJobFinishTrigger); err != nil {
		t.Fatal(err)
	}

	job, err := m.EnqueuePayload(model.JobScan, "lib", "")
	if err != nil {
		t.Fatal(err)
	}

	ev := waitForTerminal(t, ch, job.ID)
	if ev.Error == "" || !strings.Contains(ev.Error, "not persisted") {
		t.Fatalf("terminal event must carry an unpersisted-outcome Error, got Type=%q Error=%q", ev.Type, ev.Error)
	}
	if !logs.contains("could not persist terminal job state") {
		t.Fatalf("expected an slog.Error record for the failed persistence write; got: %v", logs.all())
	}
}

// Cancelling a still-queued job when the terminal write fails behaves the same:
// the canceled event carries the unpersisted-outcome Error and the failure is
// logged.
func TestFinish_CancelQueuedButPersistFails(t *testing.T) {
	oldGrace := shutdownGrace
	shutdownGrace = 100 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = oldGrace })

	d := testDB(t)
	logs := captureLogs(t)
	m := New(d, 1)
	defer m.Shutdown()

	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	m.Register(model.JobScan, func(ctx context.Context, j model.Job, p *Progress) error {
		<-release // pin the single worker so the victim stays queued
		return nil
	})

	ch, unsub, _ := m.Subscribe("", 0)
	defer unsub()

	if _, err := m.EnqueuePayload(model.JobScan, "lib", ""); err != nil { // pins the worker
		t.Fatal(err)
	}
	victim, err := m.EnqueuePayload(model.JobScan, "lib", "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // let the worker pick up the blocker

	if _, err := d.Exec(blockJobFinishTrigger); err != nil {
		t.Fatal(err)
	}

	m.Cancel(victim.ID)

	ev := waitForTerminal(t, ch, victim.ID)
	if ev.Type != EventCanceled {
		t.Fatalf("terminal event type = %q, want canceled", ev.Type)
	}
	if ev.Error == "" || !strings.Contains(ev.Error, "not persisted") {
		t.Fatalf("canceled event must carry an unpersisted-outcome Error, got %q", ev.Error)
	}
	if !logs.contains("could not persist terminal job state") {
		t.Fatalf("expected an slog.Error record for the failed persistence write; got: %v", logs.all())
	}
}

// Shutdown racing a flood of Enqueue calls must never panic on a closed
// channel; every call either enqueues or reports ErrShuttingDown.
func TestShutdownEnqueueRace(t *testing.T) {
	d := testDB(t)
	m := New(d, 2)
	m.Register(model.JobScan, func(ctx context.Context, j model.Job, p *Progress) error { return nil })

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.EnqueuePayload(model.JobScan, "lib", "")
		}()
	}
	time.Sleep(time.Millisecond)
	m.Shutdown()
	wg.Wait() // completes without panic
}

// Enqueue after Shutdown fails cleanly.
func TestEnqueueAfterShutdown(t *testing.T) {
	d := testDB(t)
	m := New(d, 1)
	m.Shutdown()
	if _, err := m.EnqueuePayload(model.JobScan, "lib", ""); err != ErrShuttingDown {
		t.Fatalf("got %v, want ErrShuttingDown", err)
	}
}

// Shutdown cancels the context of a running job so a ctx-aware handler winds
// down, and the job is recorded as canceled.
func TestShutdownCancelsRunningJob(t *testing.T) {
	d := testDB(t)
	m := New(d, 1)

	started := make(chan struct{})
	m.Register(model.JobScan, func(ctx context.Context, j model.Job, p *Progress) error {
		close(started)
		<-ctx.Done() // only returns when Shutdown cancels us
		return ctx.Err()
	})

	job, err := m.EnqueuePayload(model.JobScan, "lib", "")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	m.Shutdown() // must cancel the handler's ctx, then return

	got, err := d.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.JobCanceled {
		t.Fatalf("running job status after shutdown = %s, want canceled", got.Status)
	}
}

// Jobs still sitting in the queue at Shutdown are marked canceled, not left
// dangling as "queued" forever, and are never started.
func TestShutdownDrainsQueuedJobs(t *testing.T) {
	d := testDB(t)
	m := New(d, 1)

	var started int32
	m.Register(model.JobScan, func(ctx context.Context, j model.Job, p *Progress) error {
		atomic.AddInt32(&started, 1)
		<-ctx.Done()
		return ctx.Err()
	})

	if _, err := m.EnqueuePayload(model.JobScan, "lib", ""); err != nil { // pins the worker
		t.Fatal(err)
	}
	queued := make([]model.Job, 0, 3)
	for i := 0; i < 3; i++ {
		j, err := m.EnqueuePayload(model.JobScan, "lib", "")
		if err != nil {
			t.Fatal(err)
		}
		queued = append(queued, j)
	}

	time.Sleep(20 * time.Millisecond) // let the worker pick up the first job

	done := make(chan struct{})
	go func() { m.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return")
	}

	for _, j := range queued {
		got, err := d.GetJob(j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.JobCanceled {
			t.Fatalf("queued job %s status = %s, want canceled", j.ID, got.Status)
		}
	}
	if n := atomic.LoadInt32(&started); n != 1 {
		t.Fatalf("expected exactly the pinned job to start, got %d", n)
	}
}

// A handler that ignores context cancellation must not be able to block
// Shutdown (and process exit) forever. Shutdown returns within ~shutdownGrace
// and the stuck job is left in its last-written, non-terminal ("running")
// state.
func TestShutdownBoundedWhenHandlerIgnoresCancel(t *testing.T) {
	old := shutdownGrace
	shutdownGrace = 60 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = old })

	d := testDB(t)
	m := New(d, 1)

	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) }) // let the goroutine unwind

	started := make(chan struct{})
	m.Register(model.JobScan, func(ctx context.Context, j model.Job, p *Progress) error {
		close(started)
		<-release // deliberately ignores ctx.Done()
		return nil
	})

	job, err := m.EnqueuePayload(model.JobScan, "lib", "")
	if err != nil {
		t.Fatal(err)
	}
	<-started

	returned := make(chan struct{})
	go func() { m.Shutdown(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown blocked on a handler that ignores cancellation")
	}

	got, err := d.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.JobRunning {
		t.Fatalf("stuck job status = %s, want running (left non-terminal)", got.Status)
	}
}

// A single worker pinned by a cancel-ignoring handler must not strand the jobs
// still sitting in the queue. Even though the shutdown grace expires with the
// worker still live, drainQueue runs unconditionally and every queued job ends
// up canceled rather than stuck as "queued" forever.
func TestShutdownDrainsQueuedJobsEvenWhenGraceExpires(t *testing.T) {
	old := shutdownGrace
	shutdownGrace = 60 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = old })

	d := testDB(t)
	m := New(d, 1)

	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) }) // let the goroutine unwind

	started := make(chan struct{})
	var startedCount int32
	m.Register(model.JobScan, func(ctx context.Context, j model.Job, p *Progress) error {
		if atomic.AddInt32(&startedCount, 1) == 1 {
			close(started)
		}
		<-release // deliberately ignores ctx.Done()
		return nil
	})

	if _, err := m.EnqueuePayload(model.JobScan, "lib", ""); err != nil { // pins the worker
		t.Fatal(err)
	}
	<-started

	queued := make([]model.Job, 0, 3)
	for i := 0; i < 3; i++ {
		j, err := m.EnqueuePayload(model.JobScan, "lib", "")
		if err != nil {
			t.Fatal(err)
		}
		queued = append(queued, j)
	}

	returned := make(chan struct{})
	go func() { m.Shutdown(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown blocked on a handler that ignores cancellation")
	}

	for _, j := range queued {
		got, err := d.GetJob(j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.JobCanceled {
			t.Fatalf("queued job %s status = %s, want canceled after grace expiry", j.ID, got.Status)
		}
	}
	if n := atomic.LoadInt32(&startedCount); n != 1 {
		t.Fatalf("expected exactly the pinned job to start, got %d", n)
	}
}

// A subscriber that never reads its channel (out + inbox both full) must not
// block publish — the publisher does only non-blocking sends and drops the
// overflow.
func TestSlowSubscriberDoesNotStallPublisher(t *testing.T) {
	d := testDB(t)
	m := New(d, 1)
	defer m.Shutdown()

	_, unsub, _ := m.Subscribe("", 0) // never read
	defer unsub()

	// Far more than out(32) + inbox(1024); if publish blocked on the full
	// subscriber this loop would hang.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ {
			m.publish(Event{JobID: "x", Type: "running"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish stalled on a full/slow SSE subscriber")
	}
}

// Shutdown must return promptly even when a dead SSE subscriber's buffers are
// already full (drainQueue and other lifecycle publishes can't block on it).
func TestShutdownPromptWithDeadSubscriber(t *testing.T) {
	d := testDB(t)
	m := New(d, 1)
	m.Register(model.JobScan, func(ctx context.Context, j model.Job, p *Progress) error { return nil })

	_, unsub, _ := m.Subscribe("", 0) // never read
	defer unsub()
	for i := 0; i < 3000; i++ { // fill out + inbox, then overflow
		m.publish(Event{JobID: "x", Type: "running"})
	}
	// Leave a couple of jobs queued so drainQueue has to publish canceled.
	for i := 0; i < 2; i++ {
		if _, err := m.EnqueuePayload(model.JobScan, "lib", ""); err != nil {
			t.Fatal(err)
		}
	}

	returned := make(chan struct{})
	go func() { m.Shutdown(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown stalled on a dead SSE subscriber")
	}
}

// A subscriber that reconnects with a Last-Event-ID gets exactly the events it
// missed replayed onto its channel, in order, and no gap is reported while the
// replay window is intact.
func TestSubscribeReplaysMissedEvents(t *testing.T) {
	d := testDB(t)
	m := New(d, 1)
	defer m.Shutdown()

	for i := 0; i < 5; i++ {
		m.publish(Event{JobID: "j", Type: "progress"})
	}
	last := atomic.LoadUint64(&m.eventSeq) // == 5

	ch, unsub, gap := m.Subscribe(m.Epoch(), last-2) // client saw through Seq 3
	defer unsub()
	if gap {
		t.Fatal("gap reported for an intact replay window")
	}

	var got []uint64
	for i := 0; i < 2; i++ {
		select {
		case e := <-ch:
			got = append(got, e.Seq)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for a replayed event")
		}
	}
	if len(got) != 2 || got[0] != last-1 || got[1] != last {
		t.Fatalf("replayed seqs = %v, want [%d %d]", got, last-1, last)
	}
	select {
	case e := <-ch:
		t.Fatalf("unexpected event past the replay window: Seq=%d", e.Seq)
	case <-time.After(50 * time.Millisecond):
	}
}

// When the events between what the client last saw and what is still buffered
// have been evicted from the ring, Subscribe reports gap so the caller knows to
// refetch authoritative state.
func TestSubscribeReportsGapWhenReplayWindowEvicted(t *testing.T) {
	old := eventRing
	eventRing = 8
	t.Cleanup(func() { eventRing = old })

	d := testDB(t)
	m := New(d, 1)
	defer m.Shutdown()

	for i := 0; i < eventRing*3; i++ {
		m.publish(Event{JobID: "j", Type: "progress"})
	}

	ch, unsub, gap := m.Subscribe(m.Epoch(), 1) // Seq 1 was evicted long ago
	defer unsub()
	if !gap {
		t.Fatal("expected gap when the replay window has been evicted")
	}
	// It still gets whatever is currently buffered, just flagged as incomplete.
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected the still-buffered events to replay")
	}
}

// A fresh client with no Last-Event-ID (sinceSeq 0) never reports a gap and
// gets no replay — it starts from the live stream.
func TestSubscribeFreshClientNoGap(t *testing.T) {
	old := eventRing
	eventRing = 8
	t.Cleanup(func() { eventRing = old })

	d := testDB(t)
	m := New(d, 1)
	defer m.Shutdown()

	for i := 0; i < eventRing*3; i++ {
		m.publish(Event{JobID: "j", Type: "progress"})
	}

	ch, unsub, gap := m.Subscribe("", 0)
	defer unsub()
	if gap {
		t.Fatal("fresh client (sinceSeq 0) must never report a gap")
	}
	select {
	case e := <-ch:
		t.Fatalf("fresh client got a replayed event Seq=%d, want none", e.Seq)
	case <-time.After(50 * time.Millisecond):
	}
}

// A job canceled while still queued is not executed and ends up canceled.
func TestCancelQueuedJob(t *testing.T) {
	d := testDB(t)
	m := New(d, 1)

	release := make(chan struct{})
	var ran int32
	m.Register(model.JobScan, func(ctx context.Context, j model.Job, p *Progress) error {
		<-release // pin the single worker
		return nil
	})
	m.Register(model.JobMatch, func(ctx context.Context, j model.Job, p *Progress) error {
		ran = 1
		return nil
	})

	blocker, err := m.EnqueuePayload(model.JobScan, "lib", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = blocker
	victim, err := m.EnqueuePayload(model.JobMatch, "lib", "")
	if err != nil {
		t.Fatal(err)
	}

	m.Cancel(victim.ID)
	close(release)
	m.Shutdown()

	if ran != 0 {
		t.Fatal("canceled-while-queued job should not have run")
	}
	got, err := d.GetJob(victim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.JobCanceled {
		t.Fatalf("victim status = %s, want canceled", got.Status)
	}
}

// Event sequence numbers restart at zero every time the process does, so a
// Last-Event-ID carried across a restart can name a sequence far above anything
// the new manager has issued. Replaying "everything past 400" against a ring
// that restarted at 1 drops every event, and the client sits on stale state —
// a job that finished during the reconnect shows as running until a manual
// refresh. The epoch prefix is what turns that into a reported gap.
func TestSubscribe_ForeignEpochReportsGapAndReplaysNothing(t *testing.T) {
	d := testDB(t)
	m := New(d, 1)
	defer m.Shutdown()

	for i := 0; i < 5; i++ {
		m.publish(Event{JobID: "j", Type: "progress"})
	}

	// A client returning after a restart: a high sequence, stamped with the
	// epoch of a previous process.
	ch, unsub, gap := m.Subscribe("previous-epoch", 400)
	defer unsub()
	if !gap {
		t.Fatal("expected a gap for a Last-Event-ID from another epoch")
	}
	select {
	case ev := <-ch:
		t.Fatalf("expected no replay across epochs, got event seq %d", ev.Seq)
	case <-time.After(50 * time.Millisecond):
	}

	// Live events still arrive; the client just has to resync its state first.
	m.publish(Event{JobID: "j", Type: "done"})
	select {
	case ev := <-ch:
		if ev.Type != EventDone {
			t.Errorf("event type = %s, want done", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no live event delivered after an epoch change")
	}
}
