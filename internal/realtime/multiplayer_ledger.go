package realtime

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/doors/holdem/multiplayer"
)

// MultiplayerLedger persists the per-hand audit trail for multiplayer door
// games: one multiplayer_hands parent row + one game_rounds child row per
// human seat at the table, all inside a single transaction. Implements
// multiplayer.Ledger so the registry can hand it to every Coordinator.
//
// Mirrors src/Night.Ms.SshServer/Doors/Multiplayer/MultiplayerGameLedger.cs
// — same row shape, same ordering, same transactional guarantee. Wallet
// debit/credit happens elsewhere (the Hold'em MP screen on buy-in/cash-out
// today); this service touches only the audit tables.
type MultiplayerLedger struct {
	Pool    *pgxpool.Pool
	Queries *gen.Queries
}

// SettleHand writes the parent + per-human rows. CPU seats (UserID == 0)
// are filtered before the transaction opens. When no humans remain, the
// call is a no-op — recording a parent row with no children would just
// occupy a row without telling us anything useful about user activity.
func (l *MultiplayerLedger) SettleHand(ctx context.Context, s multiplayer.Settlement) error {
	humans := make([]multiplayer.PlayerMovement, 0, len(s.Movements))
	for _, m := range s.Movements {
		if m.UserID != 0 {
			humans = append(humans, m)
		}
	}
	if len(humans) == 0 {
		return nil
	}

	tx, err := l.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mp ledger: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := l.Queries.WithTx(tx)

	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	handID, err := q.InsertMultiplayerHand(ctx, gen.InsertMultiplayerHandParams{
		GameKey:   s.GameKey,
		TableID:   s.TableID,
		HandNo:    s.HandNumber,
		Details:   s.Details,
		SettledAt: now,
	})
	if err != nil {
		return fmt.Errorf("mp ledger: insert hand (table=%d hand=%d): %w", s.TableID, s.HandNumber, err)
	}

	for _, m := range humans {
		net := m.Payout - m.Wagered
		if err := q.InsertGameRoundWithHand(ctx, gen.InsertGameRoundWithHandParams{
			UserID:   m.UserID,
			GameKey:  s.GameKey,
			Bet:      m.Wagered,
			Payout:   m.Payout,
			Net:      net,
			Details:  nil, // heavy JSON lives once on the multiplayer_hands parent
			PlayedAt: now,
			HandID:   &handID,
		}); err != nil {
			return fmt.Errorf("mp ledger: insert round (user=%d): %w", m.UserID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("mp ledger: commit: %w", err)
	}
	return nil
}
