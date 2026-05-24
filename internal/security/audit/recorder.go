package audit

import (
	"context"
	"encoding/json"
	"expvar"
	"log/slog"
	"sync"
	"time"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// auditDroppedCounter is the expvar metric tracking how many events were
// dropped because the audit buffer was full. Visible via /debug/vars when
// NIGHTMS_DEBUG_ADDR is set. Named at package init so /debug/vars surfaces
// it from the first request, not after the first drop.
var auditDroppedCounter = expvar.NewInt("audit_events_dropped_total")

// Recorder is the audit-emission contract. Production wires a *PostgresRecorder;
// tests can substitute a stub.
type Recorder interface {
	Record(ctx context.Context, ev Event)
}

// NoopRecorder discards every event. Useful for tests and for short-lived
// helper binaries (smoketest, wsprobe) that don't need an audit pipeline.
type NoopRecorder struct{}

func (NoopRecorder) Record(context.Context, Event) {}

// PostgresRecorder writes synchronously to slog (always — never lost) and
// asynchronously to Postgres (best-effort — dropped on buffer overflow so
// the auth hot path doesn't back-pressure).
//
// Lifecycle: construct via NewPostgresRecorder → call Run(ctx) on a
// goroutine. Run blocks until ctx is done, then drains the buffer with a
// bounded shutdown deadline so in-flight events make it to Postgres before
// the process exits.
type PostgresRecorder struct {
	queries *gen.Queries
	logger  *slog.Logger
	buf     chan Event

	// shutdownTimeout caps the post-cancel drain window. Defaults to 3s in
	// NewPostgresRecorder; large enough to flush thousands of events,
	// small enough that a stuck DB doesn't delay process exit forever.
	shutdownTimeout time.Duration

	// dropWarnMu rate-limits the "audit buffer full" warn line to at most
	// once per second so a sustained flood doesn't drown the log itself.
	dropWarnMu       sync.Mutex
	dropWarnLastSent time.Time
}

// NewPostgresRecorder constructs a recorder with the given buffer size.
// Buffer size <= 0 defaults to 2048 — small per event, total ~hundreds of KB
// even under a sustained flood, lets a several-second DB blip pass without
// drops.
func NewPostgresRecorder(queries *gen.Queries, logger *slog.Logger, bufferSize int) *PostgresRecorder {
	if bufferSize <= 0 {
		bufferSize = 2048
	}
	return &PostgresRecorder{
		queries:         queries,
		logger:          logger,
		buf:             make(chan Event, bufferSize),
		shutdownTimeout: 3 * time.Second,
	}
}

// Record emits an event. Always synchronous-slog; best-effort-Postgres. Never
// blocks the caller.
func (r *PostgresRecorder) Record(_ context.Context, ev Event) {
	r.emitSlog(ev)
	select {
	case r.buf <- ev:
	default:
		auditDroppedCounter.Add(1)
		r.maybeWarnDrop(ev)
	}
}

// Run drives the background Postgres writer until ctx is done, then drains
// the buffer with shutdownTimeout. Returns nothing — diagnostics flow
// through the logger.
func (r *PostgresRecorder) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			r.drain()
			return
		case ev := <-r.buf:
			r.writeOne(ev)
		}
	}
}

// drain consumes any remaining events with shutdownTimeout cap.
func (r *PostgresRecorder) drain() {
	deadline := time.Now().Add(r.shutdownTimeout)
	for {
		select {
		case ev := <-r.buf:
			r.writeOne(ev)
		default:
			return
		}
		if time.Now().After(deadline) {
			n := len(r.buf)
			if n > 0 {
				r.logger.Warn("audit drain: deadline exceeded, dropping remaining events", "remaining", n)
			}
			return
		}
	}
}

// writeOne persists a single event. Uses a fresh context.Background-derived
// context with a per-write timeout so a stuck DB doesn't block the writer
// goroutine — and so a fast-disconnect attacker can't cancel their own
// audit event by closing the conn (the per-call ctx passed to Record is
// intentionally ignored here for that reason).
func (r *PostgresRecorder) writeOne(ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	handle, ip := ev.Subject()
	var handlePtr, ipPtr *string
	if handle != "" {
		handlePtr = &handle
	}
	if ip != "" {
		ipPtr = &ip
	}
	var details []byte
	if d := ev.Details(); d != nil {
		b, err := json.Marshal(d)
		if err != nil {
			r.logger.Warn("audit: marshal details", "event_type", ev.EventType(), "err", err)
			b = []byte("null")
		}
		details = b
	}
	if err := r.queries.InsertSecurityEvent(ctx, gen.InsertSecurityEventParams{
		EventType: ev.EventType(),
		Severity:  ev.Severity(),
		Handle:    handlePtr,
		IpAddr:    ipPtr,
		Details:   details,
	}); err != nil {
		r.logger.Warn("audit: insert", "event_type", ev.EventType(), "err", err)
	}
}

// emitSlog prints the structured slog line corresponding to ev. Routed
// through the level matching ev.Severity so external log shippers can
// filter on level.
func (r *PostgresRecorder) emitSlog(ev Event) {
	handle, ip := ev.Subject()
	attrs := []any{
		"event_type", ev.EventType(),
		"severity", ev.Severity(),
	}
	if handle != "" {
		attrs = append(attrs, "handle", handle)
	}
	if ip != "" {
		attrs = append(attrs, "ip", ip)
	}
	if d := ev.Details(); d != nil {
		attrs = append(attrs, "details", d)
	}
	switch ev.Severity() {
	case SeverityCrit:
		r.logger.Error("security event", attrs...)
	case SeverityWarn:
		r.logger.Warn("security event", attrs...)
	default:
		r.logger.Info("security event", attrs...)
	}
}

// maybeWarnDrop logs "buffer full" at most once per second so a sustained
// flood doesn't drown the very log we're trying to keep.
func (r *PostgresRecorder) maybeWarnDrop(ev Event) {
	r.dropWarnMu.Lock()
	defer r.dropWarnMu.Unlock()
	if time.Since(r.dropWarnLastSent) < time.Second {
		return
	}
	r.dropWarnLastSent = time.Now()
	r.logger.Warn("audit buffer full, dropping events",
		"event_type", ev.EventType(),
		"dropped_total", auditDroppedCounter.Value())
}
