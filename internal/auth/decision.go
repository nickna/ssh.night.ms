package auth

import "time"

// Decision is the result of an authentication lookup. Five concrete variants
// (Known, SignupRequired, Banned, RateLimited, Refused) mirror the .NET
// AuthDecision discriminated union from src/Night.Ms.SshTransport/AuthDecision.cs.
//
// Consumers (the wish auth middleware in particular) type-switch on the concrete
// type rather than read a Kind field — Go's type switches are exhaustive enough
// in practice and the pattern stays close to the C# original.
type Decision interface{ isDecision() }

// Known: the principal is authenticated. UserID is the row in users; Handle is the
// canonical handle as stored (citext-case preserved). IsSysop carries the role bit.
type Known struct {
	UserID  int64
	Handle  string
	IsSysop bool

	// OfferedFingerprint/Algorithm/Blob carry forward an SSH key the client offered
	// but did not use to authenticate (e.g., they fell through to password). The
	// session can later surface a key-adoption prompt with this info. Empty when
	// the auth method used was the same as the offer.
	OfferedFingerprint string
	OfferedAlgorithm   string
	OfferedBlob        []byte
}

// SignupRequired: handle is not yet registered. The session should land in the
// TOFU register screen. If the client offered a key, carry it forward so the
// register screen can pre-populate the key-adoption checkbox.
type SignupRequired struct {
	Handle             string
	OfferedFingerprint string
	OfferedAlgorithm   string
	OfferedBlob        []byte
	OfferedPassword    string // typed during password auth attempt; lets register pre-fill
}

// Banned: the user row has is_banned = true. Reject the session.
type Banned struct {
	Reason string
}

// RateLimited: too many recent failed attempts. The session should be rejected
// and the client told to retry after the given delay.
type RateLimited struct {
	RetryAfter time.Duration
}

// Refused: this specific credential failed. The transport can advertise another
// auth method (password fallback after publickey refusal).
type Refused struct {
	Reason string
}

func (Known) isDecision()          {}
func (SignupRequired) isDecision() {}
func (Banned) isDecision()         {}
func (RateLimited) isDecision()    {}
func (Refused) isDecision()        {}
