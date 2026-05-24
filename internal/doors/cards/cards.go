// Package cards is the shared 52-card deck + hand evaluator used by every
// card-based game (video poker now, blackjack + hold'em next). Kept in one
// place because the evaluator needs to be auditable end-to-end (paytables
// depend on its correctness).
package cards

import (
	"sort"

	"github.com/nickna/ssh.night.ms/internal/doors"
)

// Suit + Rank are int constants ordered so Ace = 14 in eval (high), with
// special-case wheel detection in Evaluate. Card is the (rank<<4 | suit)
// packed form to keep array-of-int comparisons cache-friendly.
type Suit int

const (
	Clubs Suit = iota
	Diamonds
	Hearts
	Spades
)

type Rank int

const (
	Two Rank = 2 + iota
	Three
	Four
	Five
	Six
	Seven
	Eight
	Nine
	Ten
	Jack
	Queen
	King
	Ace
)

type Card struct {
	Rank Rank
	Suit Suit
}

// String returns "A♠", "K♥" etc. — short, two-rune-ish, fits in a card cell.
func (c Card) String() string {
	const rankStr = "  23456789TJQKA"
	const suitStr = "♣♦♥♠"
	r := rankStr[c.Rank]
	s := []rune(suitStr)[c.Suit]
	return string([]rune{rune(r), s})
}

// NewDeck returns a fresh ordered 52-card deck. Shuffle yourself.
func NewDeck() []Card {
	deck := make([]Card, 0, 52)
	for s := Clubs; s <= Spades; s++ {
		for r := Two; r <= Ace; r++ {
			deck = append(deck, Card{Rank: r, Suit: s})
		}
	}
	return deck
}

// Shuffle does Fisher–Yates against the supplied RNG. The same RNG type drives
// every game so audit trails stay consistent.
func Shuffle(deck []Card, rng doors.CryptoRng) {
	for i := len(deck) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		deck[i], deck[j] = deck[j], deck[i]
	}
}

// HandRank enumerates the standard five-card poker hand classes. Higher value
// always beats lower, so callers can compare with > directly.
type HandRank int

const (
	HighCard HandRank = iota
	JacksOrBetter
	TwoPair
	ThreeOfAKind
	Straight
	Flush
	FullHouse
	FourOfAKind
	StraightFlush
	RoyalFlush
)

// String is for paytable display, not protocol. Names match the .NET stack.
func (h HandRank) String() string {
	switch h {
	case JacksOrBetter:
		return "Jacks or Better"
	case TwoPair:
		return "Two Pair"
	case ThreeOfAKind:
		return "Three of a Kind"
	case Straight:
		return "Straight"
	case Flush:
		return "Flush"
	case FullHouse:
		return "Full House"
	case FourOfAKind:
		return "Four of a Kind"
	case StraightFlush:
		return "Straight Flush"
	case RoyalFlush:
		return "Royal Flush"
	}
	return ""
}

// EvaluateBest takes 6 or 7 cards (hold'em board + hole) and returns the
// strongest 5-card HandRank it can make, plus a tiebreaker score so two
// hands of the same rank can be ordered. The tiebreaker is a sort of the
// chosen ranks descending, packed into a single int (highest card in the
// top nibble) — that gives lexicographic comparison via simple > on ints.
// O(C(7,5)) = 21 subsets; cheap to brute-force.
func EvaluateBest(hand []Card) (rank HandRank, tiebreak int) {
	rank, tiebreak, _ = EvaluateBestIndices(hand)
	return rank, tiebreak
}

// EvaluateBestIndices returns the same rank+tiebreak as EvaluateBest plus
// the 5 indices (into hand) that form the winning subset. Screens use the
// indices to highlight which specific cards made the hand at showdown.
// Returns nil indices when hand has fewer than 5 cards.
func EvaluateBestIndices(hand []Card) (rank HandRank, tiebreak int, indices []int) {
	n := len(hand)
	if n < 5 {
		return HighCard, 0, nil
	}
	if n == 5 {
		return Evaluate(hand), tiebreakOf(hand), []int{0, 1, 2, 3, 4}
	}
	bestRank := HighCard
	bestTB := 0
	bestIdx := []int{0, 1, 2, 3, 4}
	subset := make([]Card, 5)
	indexes := combinations(n, 5)
	for _, idx := range indexes {
		for i, j := range idx {
			subset[i] = hand[j]
		}
		r := Evaluate(subset)
		tb := tiebreakOf(subset)
		if r > bestRank || (r == bestRank && tb > bestTB) {
			bestRank = r
			bestTB = tb
			bestIdx = append(bestIdx[:0], idx...)
		}
	}
	out := make([]int, len(bestIdx))
	copy(out, bestIdx)
	return bestRank, bestTB, out
}

// tiebreakOf compresses the 5 ranks (descending) into a single int with
// rank-major ordering. With each rank ≤ 14 that fits in 4 bits, the 5-rank
// packing is 20 bits and never overflows. Note: this is a coarse tiebreak —
// it doesn't distinguish e.g. "pair of aces with king kicker" vs "pair of
// aces with queen kicker" by pair *first* then kickers (it sorts all 5
// descending). Good enough for HU showdowns; revisit if multi-way ties
// surface.
func tiebreakOf(hand []Card) int {
	ranks := make([]int, 0, 5)
	for _, c := range hand {
		ranks = append(ranks, int(c.Rank))
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ranks)))
	tb := 0
	for _, r := range ranks {
		tb = tb<<4 | r
	}
	return tb
}

// combinations enumerates 5-subsets of indexes [0,n). Cached for n=6,7
// since those are the only callers; the result slice is shared across calls
// for that n.
var combosCache = map[int][][]int{}

func combinations(n, k int) [][]int {
	if c, ok := combosCache[n]; ok {
		return c
	}
	var out [][]int
	idx := make([]int, k)
	var rec func(start, depth int)
	rec = func(start, depth int) {
		if depth == k {
			cp := make([]int, k)
			copy(cp, idx)
			out = append(out, cp)
			return
		}
		for i := start; i <= n-(k-depth); i++ {
			idx[depth] = i
			rec(i+1, depth+1)
		}
	}
	rec(0, 0)
	combosCache[n] = out
	return out
}

// Evaluate classifies a 5-card hand. Used by video poker directly; the
// 7-card hold'em evaluator reuses this by iterating 5-card subsets.
func Evaluate(hand []Card) HandRank {
	if len(hand) != 5 {
		return HighCard
	}
	ranks := make([]int, len(hand))
	suits := make([]Suit, len(hand))
	for i, c := range hand {
		ranks[i] = int(c.Rank)
		suits[i] = c.Suit
	}
	sort.Ints(ranks)

	flush := true
	for _, s := range suits[1:] {
		if s != suits[0] {
			flush = false
			break
		}
	}

	straight := isStraight(ranks)
	wheel := ranks[0] == 2 && ranks[1] == 3 && ranks[2] == 4 && ranks[3] == 5 && ranks[4] == int(Ace)
	if wheel {
		straight = true
	}

	counts := map[int]int{}
	for _, r := range ranks {
		counts[r]++
	}
	pairs, trips, quads := 0, 0, 0
	pairHigh := 0
	for r, c := range counts {
		switch c {
		case 4:
			quads++
		case 3:
			trips++
		case 2:
			pairs++
			if r > pairHigh {
				pairHigh = r
			}
		}
	}

	switch {
	case flush && straight && ranks[0] == int(Ten):
		return RoyalFlush
	case flush && straight:
		return StraightFlush
	case quads == 1:
		return FourOfAKind
	case trips == 1 && pairs == 1:
		return FullHouse
	case flush:
		return Flush
	case straight:
		return Straight
	case trips == 1:
		return ThreeOfAKind
	case pairs == 2:
		return TwoPair
	case pairs == 1 && pairHigh >= int(Jack):
		return JacksOrBetter
	}
	return HighCard
}

func isStraight(ranks []int) bool {
	for i := 1; i < len(ranks); i++ {
		if ranks[i] != ranks[i-1]+1 {
			return false
		}
	}
	return true
}
