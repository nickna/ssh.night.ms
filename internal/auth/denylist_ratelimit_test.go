package auth

import (
	"context"
	"net"
	"testing"

	"github.com/nickna/ssh.night.ms/internal/security/netlimit"
)

// recordingLimiter is a spy RateLimiter that captures every RecordFailure
// call so tests can assert the per-IP counter was bumped.
type recordingLimiter struct {
	calls []recordedFailure
}

type recordedFailure struct {
	handle string
	ip     string
}

func (r *recordingLimiter) Check(_ context.Context, _ string, _ net.Addr) (RateLimitCheck, error) {
	return RateLimitCheck{}, nil
}

func (r *recordingLimiter) RecordFailure(_ context.Context, handle string, sourceIP net.Addr) error {
	r.calls = append(r.calls, recordedFailure{handle: handle, ip: netlimit.CollapseIP(sourceIP)})
	return nil
}

func (r *recordingLimiter) Clear(_ context.Context, _ string) error { return nil }

func tcpAddr(ip string) net.Addr {
	return &net.TCPAddr{IP: net.ParseIP(ip), Port: 4242}
}

// TestDenylistRecordsIPFailure is the regression test for the rate-limit
// bypass: a scanner spraying denylisted system names (root, admin, …) was
// short-circuited by checkUsernameDenylist before RecordFailure ever ran, so
// the per-IP failure counter never moved and the IP never locked out or
// escalated to a persistent ban. The denylist path must now bump the IP-only
// counter (handle "") while still refusing.
func TestDenylistRecordsIPFailure(t *testing.T) {
	lim := &recordingLimiter{}
	l := &Lookup{
		Limiter:  lim,
		Denylist: NewUsernameDenylist(DefaultDeniedUsernames),
	}

	d := l.checkUsernameDenylist(context.Background(), "root", tcpAddr("51.81.187.138"))

	ref, ok := d.(Refused)
	if !ok || ref.Reason != denylistRefuseReason {
		t.Fatalf("decision = %#v, want Refused{%q}", d, denylistRefuseReason)
	}
	if len(lim.calls) != 1 {
		t.Fatalf("RecordFailure called %d times, want 1", len(lim.calls))
	}
	if lim.calls[0].handle != "" {
		t.Errorf("RecordFailure handle = %q, want \"\" (IP-only counter)", lim.calls[0].handle)
	}
	if lim.calls[0].ip != "51.81.187.138" {
		t.Errorf("RecordFailure ip = %q, want 51.81.187.138", lim.calls[0].ip)
	}
}

// TestDenylistMissDoesNotRecord pins the negative: a handle that is NOT on the
// denylist returns nil (proceed) and records nothing — the IP-failure bump is
// exclusive to the denylist short-circuit.
func TestDenylistMissDoesNotRecord(t *testing.T) {
	lim := &recordingLimiter{}
	l := &Lookup{
		Limiter:  lim,
		Denylist: NewUsernameDenylist(DefaultDeniedUsernames),
	}

	if d := l.checkUsernameDenylist(context.Background(), "alice", tcpAddr("51.81.187.138")); d != nil {
		t.Fatalf("decision = %#v, want nil (not denylisted)", d)
	}
	if len(lim.calls) != 0 {
		t.Fatalf("RecordFailure called %d times for a non-denylisted handle, want 0", len(lim.calls))
	}
}

// TestDenylistNilLimiterSafe guards the construction used by tests/tools that
// wire a denylist without a limiter — the IP-failure bump must be nil-safe.
func TestDenylistNilLimiterSafe(t *testing.T) {
	l := &Lookup{Denylist: NewUsernameDenylist(DefaultDeniedUsernames)}
	if d := l.checkUsernameDenylist(context.Background(), "root", tcpAddr("51.81.187.138")); d == nil {
		t.Fatal("expected Refused for denylisted handle, got nil")
	}
}
