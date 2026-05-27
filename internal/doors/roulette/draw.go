package roulette

// Rng is the random-pocket source Draw consumes. doors.CryptoRng satisfies
// it; tests pass a stub returning a fixed integer.
type Rng interface {
	Intn(n int) int
}

// Draw picks one pocket uniformly at random across all 38 pockets. The
// interface-typed argument lets tests swap in a deterministic source
// without depending on crypto/rand.
func Draw(rng Rng) Pocket {
	n := rng.Intn(PocketCount)
	return Pocket(n)
}
