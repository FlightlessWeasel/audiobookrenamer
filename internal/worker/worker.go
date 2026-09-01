// Package worker runs background jobs (scans, organize runs) off an in-memory
// queue and broadcasts progress events to subscribers (used by the SSE
// endpoint). Recent events are kept in a bounded replay buffer so a subscriber
// that briefly disconnects (or whose inbox overflowed) can recover the
// lifecycle events it missed by reconnecting with a Last-Event-ID. Job state is
// persisted via the db package so it survives restarts as history, though
// queued work does not resume automatically.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
)

// ErrShuttingDown is returned by Enqueue when the manager is stopping.
var ErrShuttingDown = errors.New("worker is shutting down")

// shutdownGrace bounds how long Shutdown waits for in-flight jobs to return
// after their contexts are canceled. A handler that ignores cancellation must
// not be able to block process exit forever. It is a var so tests can shrink
// it.
var shutdownGrace = 30 * time.Second

// subInbox is the per-subscriber buffer publish() feeds. It is >= eventRing so
// Subscribe can seed a reconnecting client's whole replay window into it
// without blocking. A full inbox still drops live events, but a client that
// reconnects with a Last-Event-ID recovers them from the replay buffer (or is
// told to reconcile when its window has already been evicted).
const subInbox = 1024

// eventRing bounds the replay buffer publish() keeps of the most recent events.
// A subscriber that reconnects with a Last-Event-ID is re-seeded with every
// buffered event past that sequence number; once an event scrolls out of the
// ring it can no longer be recovered individually and the client is told to
// reconcile instead. It is a var so tests can shrink it.
var eventRing = 512

// Handler executes one job. It should call p.Progress as it advances and return
// an error to mark the job failed. Returning ctx.Err() after cancellation marks
// the job canceled.
type Handler func(ctx context.Context, j model.Job, p *Progress) error

// Progress is passed to a Handler to report incremental progress.
type Progress struct {
	mgr   *Manager
	jobID string
}

// Set reports done/total counts and a status message for the job. The argument
// order matches organize.ProgressFunc so a *Progress can be passed straight
// through as one: two adjacent ints in opposite orders across a package
// boundary is a silent-swap waiting to happen.
func (p *Progress) Set(done, total int, message string) {
	// Progress counters are advisory: the terminal FinishJob write (via
	// Manager.finish) is the source of truth for job status, and the event
	// below still carries the live numbers. A failed counter write is logged,
	// not fatal — no retry loop.
	if err := p.mgr.db.UpdateJobProgress(p.jobID, model.JobRunning, total, done, message); err != nil {
		slog.Warn("could not persist job progress", "job", p.jobID, "err", err)
	}
	p.mgr.publish(Event{JobID: p.jobID, Type: EventProgress, Total: total, Done: done, Message: message})
}

// EventType is the kind of a job lifecycle Event. Its underlying type is string
// so the SSE wire format (and internal/api/sse.go / web/src/pages/Activity.tsx)
// is unchanged.
type EventType string

const (
	EventQueued   EventType = "queued"
	EventRunning  EventType = "running"
	EventProgress EventType = "progress"
	EventDone     EventType = "done"
	EventFailed   EventType = "failed"
	EventCanceled EventType = "canceled"
)

// Event is a job lifecycle notification delivered to subscribers. Seq is a
// process-global monotonic sequence number assigned by publish(); the SSE layer
// emits it as the frame id: so a reconnecting client can ask for everything
// after the last one it saw.
type Event struct {
	Seq     uint64    `json:"seq"`
	JobID   string    `json:"job_id"`
	Type    EventType `json:"type"`
	Total   int       `json:"total,omitempty"`
	Done    int       `json:"done,omitempty"`
	Message string    `json:"message,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// subscriber decouples a slow SSE reader from the publisher. publish() only
// ever does a non-blocking send into inbox; a per-subscriber pump goroutine
// forwards inbox -> out, blocking on out. So a stalled reader backs up into its
// own inbox and, at worst, drops its own events — it can never stall a worker
// goroutine or Shutdown.
type subscriber struct {
	out       chan Event
	inbox     chan Event
	done      chan struct{}
	closeOnce sync.Once
}

func (s *subscriber) stop() { s.closeOnce.Do(func() { close(s.done) }) }

func (s *subscriber) pump() {
	for {
		select {
		case <-s.done:
			return
		case e := <-s.inbox:
			select {
			case s.out <- e:
			case <-s.done:
				return
			}
		}
	}
}

// Manager owns the worker pool, the handler registry, and the subscriber set.
type Manager struct {
	db       *db.DB
	handlers map[model.JobType]Handler
	queue    chan job
	quit     chan struct{} // closed by Shutdown to stop the worker loops

	stopMu  sync.Mutex // serializes Enqueue against Shutdown
	stopped bool

	transMu sync.Mutex // serializes Cancel against a job's queued->running transition
	cancels sync.Map   // jobID -> context.CancelFunc (running jobs)
	skip    sync.Map   // jobID -> struct{} for jobs canceled while still queued

	mu   sync.Mutex
	subs map[*subscriber]struct{}

	// epoch identifies this process's event numbering. eventSeq restarts at 0
	// every time the manager is created, so a sequence number is only
	// comparable against one epoch; the SSE layer emits both in the frame id
	// and hands both back to Subscribe on reconnect.
	epoch    string
	eventSeq uint64 // atomic; last sequence number handed out by publish

	ringMu sync.Mutex
	ring   []Event // replay buffer, oldest first, capped at eventRing

	wg sync.WaitGroup
}

type job struct {
	model model.Job
}

// New creates a Manager with the given number of workers (min 1).
func New(database *db.DB, workers int) *Manager {
	if workers < 1 {
		workers = 1
	}
	m := &Manager{
		db:       database,
		handlers: map[model.JobType]Handler{},
		queue:    make(chan job, 128),
		quit:     make(chan struct{}),
		subs:     map[*subscriber]struct{}{},
		epoch:    strconv.FormatInt(time.Now().UnixNano(), 36),
	}
	for i := 0; i < workers; i++ {
		m.wg.Add(1)
		go m.loop()
	}
	return m
}

// Register binds a handler to a job type. Not safe to call after Enqueue.
func (m *Manager) Register(t model.JobType, h Handler) { m.handlers[t] = h }

// Epoch identifies this manager's event numbering; see the epoch field. It is
// fixed for the manager's lifetime and safe to read concurrently.
func (m *Manager) Epoch() string { return m.epoch }

// Enqueue creates a job row and schedules it. It returns the created job.
func (m *Manager) Enqueue(t model.JobType, libraryID string) (model.Job, error) {
	return m.EnqueuePayload(t, libraryID, "")
}

// EnqueuePayload is Enqueue with a type-specific JSON payload attached.
func (m *Manager) EnqueuePayload(t model.JobType, libraryID, payload string) (model.Job, error) {
	// Hold stopMu across the whole enqueue: Shutdown must also take it, so it
	// cannot begin (and cannot close quit / drain the queue) while a job is
	// being created and handed to the channel. That closes the race where a
	// job row is created but the send loses to a concurrent shutdown, leaving
	// it stranded as "queued" forever.
	m.stopMu.Lock()
	defer m.stopMu.Unlock()
	if m.stopped {
		return model.Job{}, ErrShuttingDown
	}
	j, err := m.db.CreateJobPayload(t, libraryID, payload)
	if err != nil {
		return model.Job{}, err
	}
	m.publish(Event{JobID: j.ID, Type: EventQueued})
	m.queue <- job{model: j} // buffered; workers keep draining until quit
	return j, nil
}

// Cancel requests cancellation of a job. A running job's context is canceled; a
// job still sitting in the queue is marked so the worker skips it. Unknown or
// already-finished jobs are ignored.
func (m *Manager) Cancel(jobID string) {
	m.transMu.Lock()
	if v, ok := m.cancels.Load(jobID); ok {
		m.transMu.Unlock()
		v.(context.CancelFunc)()
		return
	}
	j, err := m.db.GetJob(jobID)
	if err != nil || j.Status != model.JobQueued {
		m.transMu.Unlock()
		return
	}
	// Not running yet: mark it so run() skips it. Because run() takes transMu
	// before it stores its cancel func, exactly one of {run sees skip} or
	// {Cancel sees cancel func} happens — the job can't both be marked canceled
	// and run to completion.
	m.skip.Store(jobID, struct{}{})
	m.transMu.Unlock()

	m.finish(jobID, model.JobCanceled, "")
}

// Subscribe returns a channel of events, an unsubscribe function, and a gap
// flag. sinceSeq is the last event sequence the caller already saw (0 for a
// fresh client with no Last-Event-ID) and sinceEpoch is the manager epoch that
// sequence was issued under; every buffered event past it is replayed into the
// channel ahead of any live event. gap is true when the caller's events cannot
// be replayed — because the replay buffer has already evicted them, or because
// they belong to a previous epoch — and the caller must then refetch
// authoritative state instead of trusting the replayed stream. A fresh client
// (sinceSeq == 0) never reports a gap.
func (m *Manager) Subscribe(sinceEpoch string, sinceSeq uint64) (<-chan Event, func(), bool) {
	// Sequence numbers only mean anything within one epoch. A client returning
	// after a restart carries a Last-Event-ID from the previous process, whose
	// numbers can sit far above anything this one has issued yet: replaying
	// "everything past 400" against a ring that restarted at 1 silently
	// discards every event, leaving a finished job displayed as running forever.
	// A foreign epoch is therefore an unreplayable gap — nothing is replayed and
	// the client is told to resync.
	foreignEpoch := sinceSeq > 0 && sinceEpoch != m.epoch
	if foreignEpoch {
		sinceSeq = 0
	}

	s := &subscriber{
		out:   make(chan Event, 32),
		inbox: make(chan Event, subInbox),
		done:  make(chan struct{}),
	}
	// Lock order: m.mu before ringMu. Holding m.mu across the seed means a
	// concurrent publish can't register-then-fan-out a live event between our
	// registration and the ring copy, which would let it jump ahead of the
	// replayed events.
	m.mu.Lock()
	m.subs[s] = struct{}{}

	m.ringMu.Lock()
	// sinceSeq 0 is a fresh client with no Last-Event-ID: it starts from the
	// live stream, with no replay and never a gap.
	gap := foreignEpoch || (sinceSeq > 0 && len(m.ring) > 0 && m.ring[0].Seq > sinceSeq+1)
	if sinceSeq > 0 {
		for _, e := range m.ring {
			if e.Seq <= sinceSeq {
				continue
			}
			select {
			case s.inbox <- e: // subInbox >= eventRing, so this always fits
			default:
			}
		}
	}
	m.ringMu.Unlock()
	m.mu.Unlock()
	go s.pump()

	return s.out, func() {
		m.mu.Lock()
		delete(m.subs, s)
		m.mu.Unlock()
		s.stop()
	}, gap
}

// Shutdown stops accepting work, cancels running jobs, waits (bounded by
// shutdownGrace) for the workers to exit, and marks anything still queued as
// canceled.
//
// Shutdown is best-effort. If a handler does not honor context cancellation
// within shutdownGrace, Shutdown logs an actionable error and returns anyway so
// the process can exit — an unbounded join is deliberately NOT done because it
// would hang process exit and trip a systemd kill. When that happens:
//
//   - the worker goroutine and any filesystem operation it has in flight keep
//     running unsupervised after Shutdown returns;
//   - the process may exit before that work completes;
//   - that partial work is NOT rolled back. The executor's rollback only runs
//     while the handler is still executing, and by this point the caller has
//     stopped waiting for it;
//   - the job row is left in its last-written "running" state.
//
// The queue is still drained (see drainQueue) so jobs that never started are
// marked canceled rather than stranded. Queued work does not auto-resume (see
// the package doc).
func (m *Manager) Shutdown() {
	m.stopMu.Lock()
	first := !m.stopped
	if first {
		m.stopped = true
		close(m.quit)
	}
	m.stopMu.Unlock()

	if first {
		m.cancels.Range(func(_, v any) bool {
			v.(context.CancelFunc)()
			return true
		})
	}

	m.waitWorkers()

	if first {
		// Drain unconditionally, even if waitWorkers timed out with a worker
		// still live. A handler that ignores cancellation is blocked inside
		// h(ctx, j, p), never in loop()'s channel receive, so it cannot consume
		// from m.queue concurrently with this. A worker still cycling loop()
		// that races drainQueue for the same queued item is also fine: loop()
		// re-checks m.quit after receiving and marks the job canceled itself.
		// Concurrent receives on m.queue are memory-safe; the only effect is
		// which goroutine records a given job as canceled. Gating this on
		// waitWorkers succeeding would instead strand every still-buffered job
		// as "queued" in the DB forever.
		m.drainQueue()
		m.stopSubs()
	}
}

// waitWorkers waits up to shutdownGrace for the worker loops to finish. It
// returns true if they all exited, false if the grace elapsed because a handler
// is ignoring context cancellation.
//
// A false return is best-effort abandonment, not a join: the offending worker
// goroutine and any filesystem operation it is running keep going unsupervised
// after this returns, the process may exit before that work finishes, and that
// partial work is NOT rolled back (the executor's rollback only runs while the
// handler is still executing). The stuck job's row stays in its last-written
// "running" state. See Shutdown. Callers proceed regardless — blocking here
// forever would hang process exit and trip a systemd kill.
func (m *Manager) waitWorkers() bool {
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(shutdownGrace):
		stuck := make([]string, 0)
		m.cancels.Range(func(k, _ any) bool {
			stuck = append(stuck, k.(string))
			return true
		})
		slog.Error("worker shutdown grace expired; job handler(s) ignoring cancellation — in-flight filesystem work will continue unsupervised and will NOT be rolled back",
			"grace", shutdownGrace, "jobs", stuck)
		return false
	}
}

// drainQueue marks every job left buffered in the queue as canceled. It runs on
// every shutdown, including when shutdownGrace expired with a worker still live.
// That is safe: a stuck handler is blocked inside its Handler call, not in
// loop()'s channel receive, so it never drains m.queue concurrently with this;
// and a worker still cycling loop() that races drainQueue for the same queued
// item re-checks m.quit after receiving and cancels that job itself. Concurrent
// receives on m.queue are memory-safe — the only effect is which goroutine
// records a given job as canceled.
func (m *Manager) drainQueue() {
	for {
		select {
		case jb := <-m.queue:
			m.finish(jb.model.ID, model.JobCanceled, "server shut down before job started")
		default:
			return
		}
	}
}

// stopSubs signals every subscriber pump to exit.
func (m *Manager) stopSubs() {
	m.mu.Lock()
	for s := range m.subs {
		s.stop()
		delete(m.subs, s)
	}
	m.mu.Unlock()
}

// publish assigns the event its sequence number, records it in the replay ring
// buffer, then fans it out to every subscriber with a purely non-blocking send
// into each subscriber's inbox. A slow SSE reader can only ever cause its own
// inbox to fill and its own (least-recent-first) live events to be dropped — it
// can never block the calling worker goroutine, Cancel, or Shutdown. A dropped
// lifecycle event is logged; the reader recovers it on reconnect from the ring
// buffer via its Last-Event-ID (or is told to reconcile if it scrolled out).
//
// Lock order: m.mu is always taken before ringMu, everywhere. The ring append
// happens while m.mu is held so it cannot interleave with a Subscribe seeding a
// new subscriber, which would otherwise let this live event jump ahead of the
// replayed ones.
func (m *Manager) publish(e Event) {
	e.Seq = atomic.AddUint64(&m.eventSeq, 1)

	m.mu.Lock()
	subs := make([]*subscriber, 0, len(m.subs))
	for s := range m.subs {
		subs = append(subs, s)
	}

	m.ringMu.Lock()
	if len(m.ring) >= eventRing {
		// Drop the oldest to make room; head-trim keeps the slice bounded.
		m.ring = append(m.ring[:0], m.ring[len(m.ring)-eventRing+1:]...)
	}
	m.ring = append(m.ring, e)
	m.ringMu.Unlock()
	m.mu.Unlock()

	for _, s := range subs {
		select {
		case s.inbox <- e:
		default:
			if e.Type != EventProgress {
				slog.Warn("SSE subscriber inbox full; dropped lifecycle event",
					"type", e.Type, "job", e.JobID)
			}
		}
	}
}

// finish is the single funnel for recording a job's terminal outcome. The jobs
// row is authoritative — /api/jobs and the post-restart row are read back from
// it — so a terminal SSE event must not claim an outcome the database never
// stored. finish persists via db.FinishJob and then publishes the matching
// terminal event; if the write fails it logs at Error and still publishes the
// terminal event, but with Error set to the persistence-failure message so the
// client learns both the outcome and that it was not durably recorded. No
// swallow, no retry loop.
func (m *Manager) finish(jobID string, status model.JobStatus, errMsg string) {
	evType := terminalEventType(status)
	if err := m.db.FinishJob(jobID, status, errMsg); err != nil {
		slog.Error("could not persist terminal job state", "job", jobID, "status", status, "err", err)
		m.publish(Event{JobID: jobID, Type: evType, Error: "job outcome not persisted: " + err.Error()})
		return
	}
	m.publish(Event{JobID: jobID, Type: evType, Error: errMsg})
}

// terminalEventType maps a terminal JobStatus to its SSE EventType.
func terminalEventType(s model.JobStatus) EventType {
	switch s {
	case model.JobDone:
		return EventDone
	case model.JobCanceled:
		return EventCanceled
	default:
		return EventFailed
	}
}

func (m *Manager) loop() {
	defer m.wg.Done()
	for {
		select {
		case <-m.quit:
			return
		case jb := <-m.queue:
			// If shutdown began between the receive above and here, don't
			// start the job — record it as canceled and let the loop exit.
			// The select's random choice means quit alone isn't enough.
			select {
			case <-m.quit:
				m.finish(jb.model.ID, model.JobCanceled, "server shut down before job started")
				return
			default:
			}
			m.run(jb.model)
		}
	}
}

func (m *Manager) run(j model.Job) {
	m.transMu.Lock()
	if _, canceled := m.skip.LoadAndDelete(j.ID); canceled {
		m.transMu.Unlock()
		return // Cancel already marked it canceled and published.
	}
	h, ok := m.handlers[j.Type]
	if !ok {
		m.transMu.Unlock()
		m.finish(j.ID, model.JobFailed, "no handler for job type "+string(j.Type))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancels.Store(j.ID, cancel)
	m.transMu.Unlock()

	defer func() {
		m.cancels.Delete(j.ID)
		cancel()
	}()

	// The queued->running transition is authoritative: /api/jobs and the
	// post-restart row are read back from it. If it can't be persisted, abort
	// the job before invoking the handler rather than run work against a row
	// that still says "queued".
	if err := m.db.UpdateJobProgress(j.ID, model.JobRunning, 0, 0, ""); err != nil {
		slog.Error("could not persist job start", "job", j.ID, "err", err)
		m.finish(j.ID, model.JobFailed, "could not record job start: "+err.Error())
		return
	}
	m.publish(Event{JobID: j.ID, Type: EventRunning})

	p := &Progress{mgr: m, jobID: j.ID}
	err := h(ctx, j, p)
	switch {
	case err == nil:
		m.finish(j.ID, model.JobDone, "")
	case ctx.Err() != nil:
		m.finish(j.ID, model.JobCanceled, "")
	default:
		slog.Error("job failed", "job", j.ID, "type", j.Type, "err", err)
		m.finish(j.ID, model.JobFailed, err.Error())
	}
}
