package auth

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// newBareCache builds a BanCache with nil dependencies — fine for tests that
// poke the in-memory map directly via putForTest. Don't call Load/AddBan/
// RemoveBan/Run on these; they'd dereference the nil queries/redis fields.
func newBareCache() *BanCache {
	return NewBanCache(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 30*time.Second)
}

func (c *BanCache) putForTest(ip string, expiry time.Time) {
	c.mu.Lock()
	c.bans[ip] = expiry
	c.mu.Unlock()
}

func TestBanCache_IsBanned_HitMissEmpty(t *testing.T) {
	c := newBareCache()
	c.putForTest("203.0.113.45", time.Now().Add(1*time.Hour))

	if banned, _ := c.IsBanned("203.0.113.45"); !banned {
		t.Fatal("present entry should report banned=true")
	}
	if banned, _ := c.IsBanned("198.51.100.1"); banned {
		t.Fatal("absent entry should report banned=false")
	}
	if banned, _ := c.IsBanned(""); banned {
		t.Fatal("empty key should report banned=false (defensive)")
	}
}

func TestBanCache_IsBanned_StaleEntryEvictedOnRead(t *testing.T) {
	c := newBareCache()
	c.putForTest("203.0.113.46", time.Now().Add(-1*time.Hour)) // already expired

	if banned, _ := c.IsBanned("203.0.113.46"); banned {
		t.Fatal("expired entry should report banned=false")
	}

	// Read-time eviction: subsequent Snapshot should not include the entry.
	if snap := c.Snapshot(); len(snap) != 0 {
		t.Fatalf("expired entry should have been evicted from map; snapshot=%v", snap)
	}
}

func TestBanCache_Snapshot_IsADefensiveCopy(t *testing.T) {
	c := newBareCache()
	c.putForTest("203.0.113.47", time.Now().Add(1*time.Hour))

	snap := c.Snapshot()
	delete(snap, "203.0.113.47")

	if banned, _ := c.IsBanned("203.0.113.47"); !banned {
		t.Fatal("mutating the snapshot must not affect the cache")
	}
}

func TestBanCache_ReturnsExpiryToCaller(t *testing.T) {
	c := newBareCache()
	expected := time.Now().Add(15 * time.Minute).Truncate(time.Second)
	c.putForTest("203.0.113.48", expected)

	banned, exp := c.IsBanned("203.0.113.48")
	if !banned {
		t.Fatal("entry should be reported banned")
	}
	if !exp.Equal(expected) {
		t.Errorf("returned expiry mismatch: want %v, got %v", expected, exp)
	}
}
