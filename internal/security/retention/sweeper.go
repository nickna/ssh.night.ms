// Package retention runs the background loop that prunes the security_events
// table on a severity-tiered schedule and reaps expired security_ip_bans rows.
//
// Why this exists: security_events is otherwise append-only — every failed
// handshake and auth attempt from internet scanner noise accumulates forever.
// At ~1K rows/day on a small public BBS that's harmless for months, but the
// growth is unbounded and scanner-driven, so a sustained botnet campaign has
// no ceiling. The synchronous slog-JSON sink in internal/security/audit
// remains the durable record; Postgres is only the queryable hot tier, so
// pruning it aggressively is safe — we're deleting the convenient-to-query
// copy, not the only copy.
//
// Policy (both windows env-driven, see internal/config):
//   - severity "info"  (handshake_failed, auth_failure — ~98% of volume):
//     retained infoTTL, default 30 days.
//   - severity "warn"/"crit" (lockouts, bans, overlimit): retained warnTTL,
//     default 365 days.
//   - a zero TTL disables pruning for that tier.
//   - expired security_ip_bans rows are reaped every tick — finishing the
//     cleanup the 000005 migration comment promised but never wired up.
//
// audit_log (sysop actions) is intentionally NOT touched: low volume, high
// forensic value, kept indefinitely.
//
// Single-replica assumption: the DELETEs are idempotent and races between
// replicas would at worst double-issue a no-op delete, so no locking is
// needed even if the BBS scales horizontally.
package retention

import (
	"context"
	"expvar"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// securityEventsPrunedCounter tracks how many security_events rows the sweeper
// has deleted process-wide. Visible via /debug/vars when NIGHTMS_DEBUG_ADDR is
// set. Named at package init so it surfaces from the first request, not after
// the first prune. Matches the auditDroppedCounter pattern in
// internal/security/audit/recorder.go.
var securityEventsPrunedCounter = expvar.NewInt("security_events_pruned_total")

// warnTierSeverities are the severities pruned under the long (warnTTL)
// retention window. "info" is handled separately under infoTTL.
var warnTierSeverities = []string{"warn", "crit"}

// Queries is the narrow slice of gen.Queries the sweeper needs. Declared here
// (rather than depending on *gen.Queries directly) so tests can inject a fake.
// *gen.Queries satisfies it.
type Queries interface {
	DeleteSecurityEventsBySeverityOlderThan(ctx context.Context, arg gen.DeleteSecurityEventsBySeverityOlderThanParams) (int64, error)
	DeleteExpiredIPBans(ctx context.Context) (int64, error)
}

// defaultInterval is used when New is given a non-positive sweep interval.
const defaultInterval = time.Hour

// Sweeper is the singleton background service. Construct with New and launch
// Run(ctx). All state lives in Postgres + the per-tick call, so it's safe to
// run as a single goroutine.
type Sweeper struct {
	queries  Queries
	logger   *slog.Logger
	interval time.Duration // sweep cadence
	infoTTL  time.Duration // retention for severity "info"; 0 = don't prune
	warnTTL  time.Duration // retention for severity "warn"/"crit"; 0 = don't prune
}

// New returns a ready Sweeper. A non-positive interval falls back to
// defaultInterval; a nil logger falls back to slog.Default. The TTLs are kept
// as-is — a zero TTL is a meaningful "disable this tier" signal, not an error.
func New(queries Queries, logger *slog.Logger, interval, infoTTL, warnTTL time.Duration) *Sweeper {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Sweeper{
		queries:  queries,
		logger:   logger,
		interval: interval,
		infoTTL:  infoTTL,
		warnTTL:  warnTTL,
	}
}

// Run is the main loop. It sweeps once shortly after start (so a long-running
// process doesn't wait a full interval for the first prune) and then on the
// ticker. Returns when ctx is cancelled. Errors during a tick are logged and
// don't abort the loop — transient DB blips shouldn't kill retention.
func (s *Sweeper) Run(ctx context.Context) {
	if err := s.tick(ctx); err != nil {
		s.logger.Warn("retention initial sweep", "err", err)
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.tick(ctx); err != nil {
				s.logger.Warn("retention sweep", "err", err)
			}
		}
	}
}

func (s *Sweeper) tick(ctx context.Context) error {
	return s.sweep(ctx, time.Now())
}

// sweep performs one round of pruning relative to now. Split from tick so the
// cutoff math is deterministically testable. The first error short-circuits
// the round; the next tick retries from scratch.
func (s *Sweeper) sweep(ctx context.Context, now time.Time) error {
	var infoPruned, warnPruned int64

	if s.infoTTL > 0 {
		n, err := s.pruneSeverity(ctx, "info", now.Add(-s.infoTTL))
		if err != nil {
			return err
		}
		infoPruned += n
	}
	if s.warnTTL > 0 {
		cutoff := now.Add(-s.warnTTL)
		for _, sev := range warnTierSeverities {
			n, err := s.pruneSeverity(ctx, sev, cutoff)
			if err != nil {
				return err
			}
			warnPruned += n
		}
	}

	bansReaped, err := s.queries.DeleteExpiredIPBans(ctx)
	if err != nil {
		return err
	}

	if infoPruned+warnPruned+bansReaped > 0 {
		s.logger.Info("retention sweep",
			"events_info", infoPruned,
			"events_warn_crit", warnPruned,
			"bans_expired", bansReaped,
		)
	}
	return nil
}

// pruneSeverity deletes one severity tier older than cutoff and folds the
// row count into the process-wide expvar counter.
func (s *Sweeper) pruneSeverity(ctx context.Context, severity string, cutoff time.Time) (int64, error) {
	n, err := s.queries.DeleteSecurityEventsBySeverityOlderThan(ctx, gen.DeleteSecurityEventsBySeverityOlderThanParams{
		Severity: severity,
		At:       pgtype.Timestamptz{Time: cutoff, Valid: true},
	})
	if err != nil {
		return 0, err
	}
	if n > 0 {
		securityEventsPrunedCounter.Add(n)
	}
	return n, nil
}
