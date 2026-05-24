package blackjack

import (
	"testing"

	"github.com/nickna/ssh.night.ms/internal/doors/cards"
)

func TestHandValue(t *testing.T) {
	tests := []struct {
		name string
		h    []cards.Card
		want int
	}{
		{"A+K → 21 (BJ)", []cards.Card{{Rank: cards.Ace, Suit: cards.Spades}, {Rank: cards.King, Suit: cards.Clubs}}, 21},
		{"A+A → 12", []cards.Card{{Rank: cards.Ace, Suit: cards.Spades}, {Rank: cards.Ace, Suit: cards.Clubs}}, 12},
		{"A+5 → 16 soft", []cards.Card{{Rank: cards.Ace, Suit: cards.Spades}, {Rank: cards.Five, Suit: cards.Clubs}}, 16},
		{"A+5+10 → 16 hard", []cards.Card{{Rank: cards.Ace, Suit: cards.Spades}, {Rank: cards.Five, Suit: cards.Clubs}, {Rank: cards.Ten, Suit: cards.Hearts}}, 16},
		{"face cards count 10", []cards.Card{{Rank: cards.Jack, Suit: cards.Spades}, {Rank: cards.Queen, Suit: cards.Clubs}}, 20},
		{"bust", []cards.Card{{Rank: cards.King, Suit: cards.Spades}, {Rank: cards.Queen, Suit: cards.Clubs}, {Rank: cards.Two, Suit: cards.Hearts}}, 22},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := HandValue(tc.h)
			if got != tc.want {
				t.Errorf("HandValue(%v) = %d, want %d", tc.h, got, tc.want)
			}
		})
	}
}

func TestPayoutMath(t *testing.T) {
	if got := Payout(100, false, PlayerBlackjack); got != 250 {
		t.Errorf("BJ payout = %d, want 250", got)
	}
	if got := Payout(100, false, PlayerWin); got != 200 {
		t.Errorf("Win payout = %d, want 200", got)
	}
	if got := Payout(100, true, PlayerWin); got != 400 {
		t.Errorf("Doubled win payout = %d, want 400", got)
	}
	if got := Payout(100, false, Push); got != 100 {
		t.Errorf("Push payout = %d, want 100", got)
	}
	if got := Payout(100, true, Push); got != 200 {
		t.Errorf("Doubled push payout = %d, want 200", got)
	}
	if got := Payout(100, false, PlayerBust); got != 0 {
		t.Errorf("Bust payout = %d, want 0", got)
	}
}
