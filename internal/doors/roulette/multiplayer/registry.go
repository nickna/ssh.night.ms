package multiplayer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	gamesmp "github.com/nickna/ssh.night.ms/internal/doors/multiplayer"
)

// SingletonKey is the row key under which the global table's snapshot lives
// in roulette_state. Hard-coded because there is exactly one roulette table
// (sharding would replace this with an owner-elected SETNX scheme; out of
// scope for v1).
const SingletonKey = "global"

// Registry owns the singleton Coordinator. Created at main() boot,
// Restore()'d from Postgres, runs for the life of the process, and
// Persist()'d on graceful shutdown.
//
// Persistence is optional — when Queries is nil the registry runs purely
// in-memory and history evaporates on restart.
type Registry struct {
	coord *Coordinator

	queries *gen.Queries
	wallet  Wallet
	logger  *slog.Logger
}

// NewRegistry constructs the registry and returns it before Run starts.
// Caller owns the coordinator's run lifetime: cancel the ctx passed to Run
// to stop the actor loop (in-flight bets refund via the shutdown hook).
// Caller should typically:
//
//	reg := NewRegistry(queries, ledger, wallet, rng, logger)
//	if err := reg.Restore(rootCtx); err != nil { ... }
//	go reg.Coordinator().Run(rootCtx)
//	defer reg.Persist(shutdownCtx)
func NewRegistry(queries *gen.Queries, ledger gamesmp.Ledger, wallet Wallet, rng Rng, logger *slog.Logger) *Registry {
	cfg := Config{
		Durations:     DefaultPhaseDurations,
		LastBetCutoff: DefaultLastBetCutoff,
		Wallet:        wallet,
		Ledger:        ledger,
		Rng:           rng,
		Logger:        logger,
	}
	coord := NewCoordinator(cfg)
	r := &Registry{
		coord:   coord,
		queries: queries,
		wallet:  wallet,
		logger:  logger,
	}
	// Wire the shutdown-refund hook so in-flight bets at ctx-cancel are
	// returned to their owners. Wallet may be nil in tests; closure no-ops.
	coord.SetOnShutdownRefund(func(ub userBet) {
		if wallet == nil {
			return
		}
		ctx, cancelRefund := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelRefund()
		if err := wallet.Credit(ctx, ub.UserID, int64(ub.Bet.Amount)); err != nil && logger != nil {
			logger.Warn("roulette: shutdown refund failed", "user", ub.UserID, "amount", ub.Bet.Amount, "err", err)
		}
	})
	return r
}

// Coordinator returns the singleton coordinator. Caller must call its Run.
func (r *Registry) Coordinator() *Coordinator { return r.coord }

// Restore reads the most recent snapshot from roulette_state and applies it
// to the coordinator's history + phase token. Called once at boot before
// Run. Idempotent when no row exists (fresh DB → no-op).
func (r *Registry) Restore(ctx context.Context) error {
	if r.queries == nil {
		return nil
	}
	body, err := r.queries.GetRouletteState(ctx, SingletonKey)
	if err != nil {
		// First boot — no row yet. pgx surfaces ErrNoRows for one-row queries
		// that match zero rows.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	var snap PersistShape
	if err := json.Unmarshal(body, &snap); err != nil {
		if r.logger != nil {
			r.logger.Warn("roulette: bad snapshot, ignoring", "err", err)
		}
		return nil
	}
	r.coord.ApplyPersistShape(snap)
	if r.logger != nil {
		r.logger.Info("roulette restored", "history", len(snap.History), "phase_token", snap.PhaseToken)
	}
	return nil
}

// Persist writes the rolling history + monotone phase token back to
// roulette_state and refunds any in-flight bets via the coordinator's
// drain hook. Called from main's shutdown sequence.
func (r *Registry) Persist(ctx context.Context) error {
	// 1. Drain in-flight bets so the wallet refunds happen before we serialise.
	for _, ub := range r.coord.DrainPending() {
		if r.wallet == nil {
			continue
		}
		refundCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if err := r.wallet.Credit(refundCtx, ub.UserID, int64(ub.Bet.Amount)); err != nil && r.logger != nil {
			r.logger.Warn("roulette: pre-persist refund failed", "user", ub.UserID, "amount", ub.Bet.Amount, "err", err)
		}
		cancel()
	}
	// 2. Snapshot history + phase token to Postgres.
	if r.queries == nil {
		return nil
	}
	body, err := json.Marshal(r.coord.PersistShape())
	if err != nil {
		return err
	}
	return r.queries.UpsertRouletteState(ctx, gen.UpsertRouletteStateParams{
		Name:     SingletonKey,
		Snapshot: body,
	})
}
