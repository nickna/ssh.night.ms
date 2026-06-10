// Package multiplayer hosts the shared-table coordinator for roulette.
// One Coordinator instance owns the global roulette table — every roulette
// player joins the same wheel, watches the same spin, sees the same
// chronologically-ordered history. Mirrors internal/doors/holdem/multiplayer
// but adapted for parallel betting instead of turn-based action.
package multiplayer

import (
	"time"

	"github.com/nickna/ssh.night.ms/internal/doors/roulette"
)

// Phase enumerates the four states of one spin cycle.
type Phase uint8

const (
	// PhaseBetting accepts PlaceBet calls until LastBetCutoff before EndsAt.
	// All clients render a countdown to "no more bets."
	PhaseBetting Phase = iota
	// PhaseNoMoreBets is the brief settle window between Betting and Spinning.
	// New bets are rejected; the actor draws the winning pocket on entry to
	// Spinning so the value is locked before any client sees the animation
	// start.
	PhaseNoMoreBets
	// PhaseSpinning broadcasts the winning pocket; clients animate the
	// race-track ribbon toward it.
	PhaseSpinning
	// PhaseReveal credits winners, writes the ledger, then loops to Betting.
	PhaseReveal
)

func (p Phase) String() string {
	switch p {
	case PhaseBetting:
		return "betting"
	case PhaseNoMoreBets:
		return "nomorebets"
	case PhaseSpinning:
		return "spinning"
	case PhaseReveal:
		return "reveal"
	}
	return "?"
}

// PhaseMsg is the broadcast wire-shape. Coordinator publishes one after every
// phase transition AND after every accepted PlaceBet (so other clients see
// the aggregate row tick up in real time). Screens render directly from the
// most recent PhaseMsg they received plus a locally-driven countdown.
//
// Persistence: the same struct is JSON-marshalled into roulette_state's
// snapshot column on graceful shutdown so the rolling history survives
// restart. PhaseToken seeds the next-boot counter so monotonicity holds.
type PhaseMsg struct {
	Phase      Phase             `json:"phase"`
	EndsAt     time.Time         `json:"ends_at"`
	PhaseToken int64             `json:"phase_token"`
	Winning    *roulette.Pocket  `json:"winning,omitempty"`   // nil during Betting/NoMoreBets
	Aggregate  map[string]int32  `json:"aggregate,omitempty"` // BetKey.String() → total chips
	History    []roulette.Pocket `json:"history,omitempty"`   // last N pockets, oldest first
	Occupants  int               `json:"occupants"`
}

// PersistShape is the minimal subset of state we serialise to Postgres on
// graceful shutdown. PhaseMsg is too dynamic (Aggregate gets reset every
// round) — only History + PhaseToken survive a restart. Bets in flight at
// shutdown are refunded explicitly via the registry's Persist closure;
// no in-flight wagers are saved here.
type PersistShape struct {
	History    []roulette.Pocket `json:"history"`
	PhaseToken int64             `json:"phase_token"`
}
