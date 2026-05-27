// Package multiplayer holds the audit-trail types every multiplayer door
// game shares with the realtime ledger. Lifted out of internal/doors/holdem/
// multiplayer so a second multiplayer game (roulette) can write
// multiplayer_hands rows without re-importing Hold'em's coordinator package.
// The Hold'em multiplayer package keeps its own type names via aliases so
// existing call sites compile unchanged.
package multiplayer

import "context"

// PlayerMovement is one seat's per-hand economic outcome. UserID == 0
// indicates a CPU seat; the ledger filters those out at persist time. Shape
// matches the legacy stack's wire format so a hand-replay tool decoding
// either stack's audit trail sees the same fields.
type PlayerMovement struct {
	UserID  int64
	Handle  string
	Wagered int32 // total committed across all wagers placed this hand
	Payout  int32 // chips won this hand (0 for losers)
	Stack   int32 // chip stack after settlement (game-specific; not always meaningful)
}

// Settlement is the per-hand payload the ledger persists. Details is a JSON
// blob with game-specific bookkeeping (board cards for Hold'em, winning
// pocket + per-bet detail for Roulette).
type Settlement struct {
	TableID    int64
	GameKey    string // "holdem-mp" | "roulette" | future
	HandNumber int64
	Movements  []PlayerMovement
	Details    []byte // JSON
}

// Ledger is the persistence contract each game's coordinator depends on. The
// realtime layer's MultiplayerLedger satisfies it; tests can pass a stub.
type Ledger interface {
	SettleHand(ctx context.Context, s Settlement) error
}
