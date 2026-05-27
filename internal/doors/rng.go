// Package doors hosts the games framework + each game's implementation:
// a wallet, a per-round ledger, a crypto-strength RNG, plus one tea.Model
// per game.
package doors

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
)

// CryptoRng wraps crypto/rand. Per-game state isn't needed — we read fresh
// entropy on every Intn call. Cheap enough for slot-style games; tighter
// designs (multiplayer Hold'em shuffle) can hold a seeded math/rand.Rand if
// determinism for testing matters.
type CryptoRng struct{}

func (CryptoRng) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read failure is fatal — the OS RNG is broken. Surface as a
		// panic; the caller can recover if it must, but games shouldn't
		// silently fall back to a weak source.
		panic(fmt.Sprintf("doors: crypto/rand: %v", err))
	}
	v := binary.LittleEndian.Uint64(b[:])
	return int(v % uint64(n))
}
