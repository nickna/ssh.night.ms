package cards

import "testing"

func TestEvaluateBestIndices_Flush(t *testing.T) {
	// Hand: hole = A♠ A♣, board = 2♠ 5♠ 9♠ K♠ 3♥.
	// Best 5 is the spade flush: indices 0 (A♠) + 2-5 (the four board spades).
	hand := []Card{
		{Ace, Spades}, {Ace, Clubs},
		{Two, Spades}, {Five, Spades}, {Nine, Spades}, {King, Spades},
		{Three, Hearts},
	}
	rank, _, idx := EvaluateBestIndices(hand)
	if rank != Flush {
		t.Fatalf("rank = %v, want Flush", rank)
	}
	got := map[int]bool{}
	for _, i := range idx {
		got[i] = true
	}
	want := map[int]bool{0: true, 2: true, 3: true, 4: true, 5: true}
	if len(got) != 5 {
		t.Fatalf("indices = %v, want exactly 5 distinct", idx)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing index %d in best-5 (got %v)", k, idx)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected index %d in best-5 (A♣ shouldn't beat 2♠ in the flush)", k)
		}
	}
}

func TestEvaluateBestIndices_FiveCards(t *testing.T) {
	// 5 cards in → all 5 indices returned.
	hand := []Card{{Ten, Spades}, {Jack, Spades}, {Queen, Spades}, {King, Spades}, {Ace, Spades}}
	rank, _, idx := EvaluateBestIndices(hand)
	if rank != RoyalFlush {
		t.Fatalf("rank = %v, want RoyalFlush", rank)
	}
	if len(idx) != 5 {
		t.Fatalf("len(idx) = %d, want 5", len(idx))
	}
	for i, v := range idx {
		if v != i {
			t.Errorf("idx[%d] = %d, want %d", i, v, i)
		}
	}
}

func TestEvaluateBest(t *testing.T) {
	// 7-card hand: pair of aces + 5 random board cards including a possible flush.
	// Should pick the better hand (here: flush).
	hand := []Card{
		{Ace, Spades}, {Ace, Clubs}, // hole
		{Two, Spades}, {Five, Spades}, {Nine, Spades}, {King, Spades}, // 4 spades on board
		{Three, Hearts},
	}
	r, _ := EvaluateBest(hand)
	if r != Flush {
		t.Errorf("EvaluateBest(7) = %v, want Flush", r)
	}
	// 7-card hand: trips beats two pair.
	hand2 := []Card{
		{Nine, Spades}, {Nine, Clubs},
		{Nine, Hearts}, {King, Diamonds}, {King, Hearts}, {Two, Clubs}, {Five, Diamonds},
	}
	r2, _ := EvaluateBest(hand2)
	if r2 != FullHouse {
		t.Errorf("EvaluateBest(trips+pair) = %v, want FullHouse", r2)
	}
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name string
		hand []Card
		want HandRank
	}{
		{"royal flush",
			[]Card{{Ten, Spades}, {Jack, Spades}, {Queen, Spades}, {King, Spades}, {Ace, Spades}},
			RoyalFlush,
		},
		{"straight flush low",
			[]Card{{Six, Hearts}, {Seven, Hearts}, {Eight, Hearts}, {Nine, Hearts}, {Ten, Hearts}},
			StraightFlush,
		},
		{"wheel straight (A-2-3-4-5)",
			[]Card{{Two, Clubs}, {Three, Hearts}, {Four, Spades}, {Five, Diamonds}, {Ace, Clubs}},
			Straight,
		},
		{"four of a kind",
			[]Card{{Nine, Clubs}, {Nine, Hearts}, {Nine, Spades}, {Nine, Diamonds}, {Two, Clubs}},
			FourOfAKind,
		},
		{"full house",
			[]Card{{King, Clubs}, {King, Hearts}, {King, Spades}, {Two, Diamonds}, {Two, Clubs}},
			FullHouse,
		},
		{"flush",
			[]Card{{Two, Spades}, {Five, Spades}, {Seven, Spades}, {Nine, Spades}, {King, Spades}},
			Flush,
		},
		{"straight high",
			[]Card{{Ten, Clubs}, {Jack, Hearts}, {Queen, Spades}, {King, Diamonds}, {Ace, Clubs}},
			Straight,
		},
		{"three of a kind",
			[]Card{{Five, Clubs}, {Five, Hearts}, {Five, Spades}, {Two, Diamonds}, {King, Clubs}},
			ThreeOfAKind,
		},
		{"two pair",
			[]Card{{Five, Clubs}, {Five, Hearts}, {Two, Spades}, {Two, Diamonds}, {King, Clubs}},
			TwoPair,
		},
		{"jacks or better — jacks",
			[]Card{{Jack, Clubs}, {Jack, Hearts}, {Two, Spades}, {Five, Diamonds}, {Nine, Clubs}},
			JacksOrBetter,
		},
		{"low pair is high card",
			[]Card{{Ten, Clubs}, {Ten, Hearts}, {Two, Spades}, {Five, Diamonds}, {Nine, Clubs}},
			HighCard,
		},
		{"high card",
			[]Card{{Two, Clubs}, {Five, Hearts}, {Seven, Spades}, {Nine, Diamonds}, {King, Clubs}},
			HighCard,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.hand)
			if got != tc.want {
				t.Errorf("Evaluate() = %v, want %v", got, tc.want)
			}
		})
	}
}
