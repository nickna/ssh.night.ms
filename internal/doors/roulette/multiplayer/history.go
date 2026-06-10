package multiplayer

import "github.com/nickna/ssh.night.ms/internal/doors/roulette"

// HistoryCap is the maximum number of past outcomes the coordinator keeps
// in memory. The TUI strip surfaces the last 10; the stats panel uses the
// fuller 100-deep ring for hot/cold and streak stats.
const HistoryCap = 100

// historyRing is a fixed-size circular buffer of pockets. Newest entry lives
// at index (head-1+len)%cap. Empty slots (before the ring has filled) are
// flagged by counting len separately from cap.
type historyRing struct {
	buf  [HistoryCap]roulette.Pocket
	head int
	len  int
}

// Push appends p to the ring, evicting the oldest entry when full.
func (r *historyRing) Push(p roulette.Pocket) {
	r.buf[r.head] = p
	r.head = (r.head + 1) % HistoryCap
	if r.len < HistoryCap {
		r.len++
	}
}

// Slice returns the last n pockets oldest-first. n is clamped to the ring's
// current length. Returns a freshly-allocated slice the caller can hold.
func (r *historyRing) Slice(n int) []roulette.Pocket {
	if n > r.len {
		n = r.len
	}
	if n <= 0 {
		return nil
	}
	out := make([]roulette.Pocket, n)
	// Walk from oldest to newest. Oldest sits at head when full; at index 0
	// when partially filled.
	start := r.head - r.len
	if start < 0 {
		start += HistoryCap
	}
	startOfWindow := start + (r.len - n)
	if startOfWindow < 0 {
		startOfWindow += HistoryCap
	}
	for i := 0; i < n; i++ {
		out[i] = r.buf[(startOfWindow+i)%HistoryCap]
	}
	return out
}

// All returns every stored pocket oldest-first.
func (r *historyRing) All() []roulette.Pocket { return r.Slice(r.len) }

// Replace overwrites the ring with the given slice (oldest-first). Used by
// the registry's Restore step when rehydrating from a Postgres snapshot.
func (r *historyRing) Replace(items []roulette.Pocket) {
	r.head = 0
	r.len = 0
	for _, p := range items {
		if r.len >= HistoryCap {
			break
		}
		r.Push(p)
	}
}

// Stats summarises the contents of the history ring. The TUI stats overlay
// renders these aggregates directly.
type Stats struct {
	TotalSpins   int
	RedCount     int
	BlackCount   int
	GreenCount   int
	LongestRed   int        // longest consecutive run of red outcomes
	LongestBlack int        // longest consecutive run of black outcomes
	Hot          []HotEntry // pockets that came up most often, top 5, descending
	Cold         []HotEntry // pockets that came up least often (still > 0), bottom 5 ascending
}

// HotEntry pairs a pocket with the number of times it appeared in the
// current history window.
type HotEntry struct {
	Pocket roulette.Pocket
	Count  int
}

// ComputeStats walks the entire history ring once to produce the stats
// payload. O(N) — fine for N ≤ HistoryCap (100).
func (r *historyRing) ComputeStats() Stats {
	all := r.All()
	stats := Stats{TotalSpins: len(all)}
	if len(all) == 0 {
		return stats
	}
	counts := make(map[roulette.Pocket]int, len(all))
	curRed, curBlack := 0, 0
	for _, p := range all {
		counts[p]++
		switch p.Color() {
		case roulette.Red:
			curRed++
			if curRed > stats.LongestRed {
				stats.LongestRed = curRed
			}
			curBlack = 0
			stats.RedCount++
		case roulette.Black:
			curBlack++
			if curBlack > stats.LongestBlack {
				stats.LongestBlack = curBlack
			}
			curRed = 0
			stats.BlackCount++
		case roulette.Green:
			curRed = 0
			curBlack = 0
			stats.GreenCount++
		}
	}
	// Build sorted slices for Hot/Cold. Simple insertion-sort keeps the file
	// dependency-free; the input is bounded to ≤38 distinct keys.
	pairs := make([]pocketCount, 0, len(counts))
	for p, c := range counts {
		pairs = append(pairs, pocketCount{p, c})
	}
	sortPairs(pairs, true) // descending by count
	for i, pr := range pairs {
		if i >= 5 {
			break
		}
		stats.Hot = append(stats.Hot, HotEntry{Pocket: pr.p, Count: pr.c})
	}
	sortPairs(pairs, false) // ascending by count
	for i, pr := range pairs {
		if i >= 5 {
			break
		}
		stats.Cold = append(stats.Cold, HotEntry{Pocket: pr.p, Count: pr.c})
	}
	return stats
}

// pocketCount is the local pair type used by ComputeStats's sort step. Named
// so we can declare sortPairs with a concrete element type.
type pocketCount struct {
	p roulette.Pocket
	c int
}

// sortPairs orders the slice by count. desc=true → descending, false → ascending.
// Stable insertion sort — n ≤ 38 so we don't need anything fancier.
func sortPairs(arr []pocketCount, desc bool) {
	for i := 1; i < len(arr); i++ {
		j := i
		for j > 0 {
			a := arr[j-1].c
			b := arr[j].c
			swap := false
			if desc {
				if a < b {
					swap = true
				}
			} else {
				if a > b {
					swap = true
				}
			}
			if !swap {
				break
			}
			arr[j-1], arr[j] = arr[j], arr[j-1]
			j--
		}
	}
}
