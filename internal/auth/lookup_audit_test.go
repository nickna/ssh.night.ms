package auth

import (
	"context"
	"testing"

	"github.com/nickna/ssh.night.ms/internal/security/audit"
)

// captureRecorder records every event it receives so tests can assert on
// what emitAuthAudit chose to emit (or suppress).
type captureRecorder struct{ events []audit.Event }

func (c *captureRecorder) Record(_ context.Context, ev audit.Event) {
	c.events = append(c.events, ev)
}

// TestEmitAuthAudit_PublickeyRefusalSuppression pins the feed-noise policy:
// the unenrolled-key offer-dance refusal is dropped from security_events,
// while the cross-account refusal (and any other refusal) is kept.
func TestEmitAuthAudit_PublickeyRefusalSuppression(t *testing.T) {
	tests := []struct {
		name     string
		decision Decision
		wantType string // "" => suppressed (no event recorded)
	}{
		{
			name:     "unenrolled key is suppressed",
			decision: Refused{Reason: keyUnenrolledReason},
			wantType: "",
		},
		{
			name:     "denylisted handle is suppressed",
			decision: Refused{Reason: denylistRefuseReason},
			wantType: "",
		},
		{
			name:     "key on another account is kept",
			decision: Refused{Reason: keyOtherAccountReason},
			wantType: "auth_failure",
		},
		{
			name:     "generic refusal is kept",
			decision: Refused{Reason: "internal error"},
			wantType: "auth_failure",
		},
		{
			name:     "success is recorded",
			decision: Known{Handle: "nbn"},
			wantType: "auth_success",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &captureRecorder{}
			l := &Lookup{Audit: rec}
			l.emitAuthAudit(context.Background(), "publickey", "nbn", nil, tc.decision)

			if tc.wantType == "" {
				if len(rec.events) != 0 {
					t.Fatalf("expected no event recorded, got %d: %+v", len(rec.events), rec.events)
				}
				return
			}
			if len(rec.events) != 1 {
				t.Fatalf("expected exactly one event, got %d: %+v", len(rec.events), rec.events)
			}
			if got := rec.events[0].EventType(); got != tc.wantType {
				t.Fatalf("event type = %q, want %q", got, tc.wantType)
			}
		})
	}
}
