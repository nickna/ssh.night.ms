package doors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// DailyAllowance is the credit count topped up each day. Generous enough for
// a few real spins; modest enough that the user has to play to climb the
// leaderboards.
const DailyAllowance = 100

// Wallet is the in-memory snapshot exposed to game screens. Two-bucket
// design: daily credits refresh, winnings persist. Bets debit daily first,
// then winnings; payouts always credit winnings.
type Wallet struct {
	UserID         int64
	DailyCredits   int32
	WinningsCents  int64
	DailyRefreshed time.Time
}

// Total returns the spendable amount (in credits, where 1 credit == 1 cent).
func (w Wallet) Total() int64 { return int64(w.DailyCredits) + w.WinningsCents }

// WalletService loads + persists Wallet rows.
type WalletService struct {
	Queries *gen.Queries
}

// Load returns the current wallet, refreshing the daily allowance if the
// last refresh was on a different UTC date than today. Persistence happens
// in this call so the user always sees a fresh balance on entry.
func (s *WalletService) Load(ctx context.Context, userID int64) (Wallet, error) {
	row, err := s.Queries.GetOrCreateWallet(ctx, gen.GetOrCreateWalletParams{
		UserID:                  userID,
		DailyCredits:            DailyAllowance,
		DailyCreditsRefreshedOn: pgtype.Date{Time: today(), Valid: true},
		UpdatedAt:               pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		return Wallet{}, fmt.Errorf("wallet: get: %w", err)
	}
	w := Wallet{
		UserID:        row.UserID,
		DailyCredits:  row.DailyCredits,
		WinningsCents: row.WinningsBalance,
	}
	if row.DailyCreditsRefreshedOn.Valid {
		w.DailyRefreshed = row.DailyCreditsRefreshedOn.Time
	}
	if !sameUTCDay(w.DailyRefreshed, today()) {
		w.DailyCredits = DailyAllowance
		w.DailyRefreshed = today()
		if err := s.persist(ctx, w); err != nil {
			return w, err
		}
	}
	return w, nil
}

// Bet deducts amount from daily credits first, falling back to winnings.
// Returns ErrInsufficient when the user can't cover the bet.
func (s *WalletService) Bet(ctx context.Context, w *Wallet, amount int32) error {
	if amount <= 0 {
		return fmt.Errorf("wallet: non-positive bet %d", amount)
	}
	cost := int64(amount)
	if cost > w.Total() {
		return ErrInsufficient
	}
	if int64(w.DailyCredits) >= cost {
		w.DailyCredits -= int32(cost)
	} else {
		// Take from daily first, top up from winnings.
		left := cost - int64(w.DailyCredits)
		w.DailyCredits = 0
		w.WinningsCents -= left
	}
	return s.persist(ctx, *w)
}

// Credit adds a payout to the winnings bucket.
func (s *WalletService) Credit(ctx context.Context, w *Wallet, amount int64) error {
	if amount < 0 {
		return fmt.Errorf("wallet: negative credit %d", amount)
	}
	w.WinningsCents += amount
	return s.persist(ctx, *w)
}

// ErrInsufficient signals the user can't afford a bet.
var ErrInsufficient = errors.New("wallet: insufficient credits")

func (s *WalletService) persist(ctx context.Context, w Wallet) error {
	refreshed := pgtype.Date{Time: w.DailyRefreshed, Valid: !w.DailyRefreshed.IsZero()}
	return s.Queries.UpdateWallet(ctx, gen.UpdateWalletParams{
		UserID:                  w.UserID,
		DailyCredits:            w.DailyCredits,
		DailyCreditsRefreshedOn: refreshed,
		WinningsBalance:         w.WinningsCents,
		UpdatedAt:               pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
}

// LedgerEntry records one round of one game. The Details blob is jsonb so
// per-game state (which reels stopped where, which cards were dealt) can be
// stored without schema migrations.
type LedgerEntry struct {
	UserID  int64
	GameKey string
	Bet     int32
	Payout  int32
	Net     int32
	Details any
}

// Record appends a game_rounds row. Failure is logged but non-fatal to the
// game flow — the wallet update is the authoritative state.
func (s *WalletService) Record(ctx context.Context, e LedgerEntry) error {
	body, err := json.Marshal(e.Details)
	if err != nil {
		body = []byte("{}")
	}
	return s.Queries.InsertGameRound(ctx, gen.InsertGameRoundParams{
		UserID:   e.UserID,
		GameKey:  e.GameKey,
		Bet:      e.Bet,
		Payout:   e.Payout,
		Net:      e.Net,
		Details:  body,
		PlayedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
}

func today() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func sameUTCDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}
