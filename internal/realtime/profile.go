package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// ProfileService is the read-side surface for chat-screen affordances that
// reach into the users table: /finger, PFP-marker batch lookup. Write-side
// profile mutations (real name, bio, etc.) live in the web layer's handler
// path today — keeping the Go service narrow lets us evolve the two surfaces
// independently while they share schema.
type ProfileService struct {
	Queries *gen.Queries
}

// NewProfileService binds the service to a sqlc query bundle.
func NewProfileService(q *gen.Queries) *ProfileService {
	return &ProfileService{Queries: q}
}

// ProfileSnapshot is the bag of "everything /finger needs to render the user
// card". Counts come from per-table COUNT(*) queries; for a BBS-scale user
// table the round-trips are fine and avoid CTE complexity.
type ProfileSnapshot struct {
	UserID                     int64
	Handle                     string
	RealName                   string
	Location                   string
	Bio                        string
	CreatedAt                  time.Time
	LastSeenAt                 time.Time // zero = never seen
	IsSysop                    bool
	TimeZoneID                 string
	TemperatureUnit            int32
	ClockFormat                int32
	DateFormat                 int32
	SuppressKeyAdoptionPrompts bool
	RequireSshKey              bool
	HasPassword                bool
	ChatMessageCount           int64
	TopicCount                 int64
	PostCount                  int64
	ProfilePictureUpdatedAt    time.Time // zero = no PFP uploaded
}

// ProfileUpdate is the write-side counterpart to ProfileSnapshot — the bag of
// fields the Profile screen submits on save. Empty RealName/Bio mean "clear
// the column" (sqlc maps "" → NULL via the *string overrides). Location is
// not in this struct: the Profile-tab location field is owned by the
// saved-locations layer (see LocationService.SetPrimaryFromGeocode), which
// mirrors its label into users.location separately so legacy readers see
// the same string they used to.
type ProfileUpdate struct {
	RealName, Bio              string
	TimeZoneID                 string
	TemperatureUnit            int32
	ClockFormat                int32
	DateFormat                 int32
	SuppressKeyAdoptionPrompts bool
	RequireSshKey              bool
}

// Field length caps. Match the legacy stack's ProfileService limits so a row
// written by either stack passes the other's validation.
const (
	MaxRealNameLength   = 64
	MaxLocationLength   = 64
	MaxBioLength        = 500
	MaxTimeZoneIDLength = 64
	MinPasswordLength   = 10
)

// ErrRequiresAtLeastOneKey is returned by UpdateProfile when a caller asks to
// enable RequireSshKey on an account that currently has zero registered SSH
// keys. Surfaced verbatim by the Profile screen.
var ErrRequiresAtLeastOneKey = errors.New("profile: enabling require-ssh-key needs at least one registered key")

// ErrInvalidProfileField is the catch-all for length/format failures during
// UpdateProfile. The wrapped message names the offending field.
var ErrInvalidProfileField = errors.New("profile: invalid field")

// HasPfp is the boolean shorthand used by the chat renderer.
func (p ProfileSnapshot) HasPfp() bool { return !p.ProfilePictureUpdatedAt.IsZero() }

// GetByHandle looks up a profile by handle (case-insensitive via citext) and
// returns the snapshot + derived counts. Returns nil when the user doesn't
// exist so callers can render a friendly "no such user" notice.
func (s *ProfileService) GetByHandle(ctx context.Context, handle string) (*ProfileSnapshot, error) {
	user, err := s.Queries.GetUserByHandle(ctx, handle)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("profile: get user: %w", err)
	}
	return s.assembleSnapshot(ctx, user)
}

// GetByID is the by-id variant of GetByHandle, used when the caller already
// has the user's row id (e.g., the Profile screen reading the logged-in
// session). Returns nil when the user no longer exists.
func (s *ProfileService) GetByID(ctx context.Context, userID int64) (*ProfileSnapshot, error) {
	user, err := s.Queries.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("profile: get user: %w", err)
	}
	return s.assembleSnapshot(ctx, user)
}

// assembleSnapshot runs the three per-user COUNT queries concurrently and
// folds them into a ProfileSnapshot. Sequential round-trips here would be
// 3× the latency floor for every /finger lookup; on a chat with many idle
// users refreshing presence, that floor turns into the bottleneck.
func (s *ProfileService) assembleSnapshot(ctx context.Context, user gen.User) (*ProfileSnapshot, error) {
	var chatCount, topicCount, postCount int64
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		n, err := s.Queries.ChatMessageCountForUser(gctx, user.ID)
		if err != nil {
			return fmt.Errorf("profile: chat count: %w", err)
		}
		chatCount = n
		return nil
	})
	g.Go(func() error {
		n, err := s.Queries.TopicCountForUser(gctx, user.ID)
		if err != nil {
			return fmt.Errorf("profile: topic count: %w", err)
		}
		topicCount = n
		return nil
	})
	g.Go(func() error {
		n, err := s.Queries.PostCountForUser(gctx, user.ID)
		if err != nil {
			return fmt.Errorf("profile: post count: %w", err)
		}
		postCount = n
		return nil
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	snap := &ProfileSnapshot{
		UserID:                     user.ID,
		Handle:                     user.Handle,
		CreatedAt:                  user.CreatedAt.Time,
		LastSeenAt:                 user.LastSeenAt.Time,
		IsSysop:                    user.IsSysop,
		TimeZoneID:                 user.TimeZoneID,
		TemperatureUnit:            user.TemperatureUnit,
		ClockFormat:                user.ClockFormat,
		DateFormat:                 user.DateFormat,
		SuppressKeyAdoptionPrompts: user.SuppressKeyAdoptionPrompts,
		RequireSshKey:              user.RequireSshKey,
		HasPassword:                len(user.PasswordHash) > 0,
		ChatMessageCount:           chatCount,
		TopicCount:                 topicCount,
		PostCount:                  postCount,
		ProfilePictureUpdatedAt:    user.ProfilePictureUpdatedAt.Time,
	}
	if user.RealName != nil {
		snap.RealName = *user.RealName
	}
	if user.Location != nil {
		snap.Location = *user.Location
	}
	if user.Bio != nil {
		snap.Bio = *user.Bio
	}
	return snap, nil
}

// UpdateProfile validates and writes every editable column from the Profile
// screen. Validation is centralized here so future API paths inherit it.
// When u.RequireSshKey is true, the call refuses with ErrRequiresAtLeastOneKey
// if the user has no Ssh credentials registered — defends against TOCTOU
// where the screen-side cache is stale.
func (s *ProfileService) UpdateProfile(ctx context.Context, userID int64, u ProfileUpdate) error {
	rn := strings.TrimSpace(u.RealName)
	bio := strings.TrimSpace(u.Bio)
	tz := strings.TrimSpace(u.TimeZoneID)

	if len(rn) > MaxRealNameLength {
		return fmt.Errorf("%w: real_name longer than %d chars", ErrInvalidProfileField, MaxRealNameLength)
	}
	if len(bio) > MaxBioLength {
		return fmt.Errorf("%w: bio longer than %d chars", ErrInvalidProfileField, MaxBioLength)
	}
	if tz == "" || len(tz) > MaxTimeZoneIDLength {
		return fmt.Errorf("%w: time_zone_id required (max %d chars)", ErrInvalidProfileField, MaxTimeZoneIDLength)
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("%w: time_zone_id %q: %v", ErrInvalidProfileField, tz, err)
	}
	if u.TemperatureUnit < 0 || u.TemperatureUnit > 2 {
		return fmt.Errorf("%w: temperature_unit must be 0..2", ErrInvalidProfileField)
	}
	if u.ClockFormat < 0 || u.ClockFormat > 1 {
		return fmt.Errorf("%w: clock_format must be 0..1", ErrInvalidProfileField)
	}
	if u.DateFormat < 0 || u.DateFormat > 2 {
		return fmt.Errorf("%w: date_format must be 0..2", ErrInvalidProfileField)
	}

	if u.RequireSshKey {
		n, err := s.Queries.CountSshCredentialsForUser(ctx, userID)
		if err != nil {
			return fmt.Errorf("profile: count ssh keys: %w", err)
		}
		if n == 0 {
			return ErrRequiresAtLeastOneKey
		}
	}

	params := gen.UpdateUserProfileParams{
		ID:                         userID,
		RealName:                   nullableTrimmed(rn),
		Bio:                        nullableTrimmed(bio),
		TimeZoneID:                 tz,
		TemperatureUnit:            u.TemperatureUnit,
		ClockFormat:                u.ClockFormat,
		DateFormat:                 u.DateFormat,
		SuppressKeyAdoptionPrompts: u.SuppressKeyAdoptionPrompts,
		RequireSshKey:              u.RequireSshKey,
	}
	if err := s.Queries.UpdateUserProfile(ctx, params); err != nil {
		return fmt.Errorf("profile: update: %w", err)
	}
	return nil
}

// nullableTrimmed maps "" → nil and any other string → &s so sqlc's *string
// overrides write NULL for empty input.
func nullableTrimmed(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ChangePassword persists a new password hash + algo and audit-logs the
// change. The caller is responsible for verifying the existing password (if
// any) and computing hash/algo via auth.Hasher.Hash.
func (s *ProfileService) ChangePassword(ctx context.Context, userID int64, hash []byte, algo string) error {
	if len(hash) == 0 {
		return fmt.Errorf("%w: empty hash", ErrInvalidProfileField)
	}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	var algoPtr *string
	if algo != "" {
		algoPtr = &algo
	}
	if err := s.Queries.UpdateUserPassword(ctx, gen.UpdateUserPasswordParams{
		ID:                userID,
		PasswordHash:      hash,
		PasswordAlgo:      algoPtr,
		PasswordUpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("profile: update password: %w", err)
	}
	targetID := userID
	if err := s.Queries.InsertAuditLog(ctx, gen.InsertAuditLogParams{
		ActorID:    &userID,
		Action:     "password.changed",
		TargetType: "user",
		TargetID:   &targetID,
		Details:    nil,
		CreatedAt:  now,
	}); err != nil {
		return fmt.Errorf("profile: audit password change: %w", err)
	}
	return nil
}

// LogKeyAdd writes the audit_log row for an SSH key addition initiated from
// the TUI. The web add path does not yet write an audit row (TODO).
func (s *ProfileService) LogKeyAdd(ctx context.Context, userID int64, fingerprint string) error {
	details, err := json.Marshal(map[string]string{
		"provider":    "Ssh",
		"fingerprint": fingerprint,
	})
	if err != nil {
		return fmt.Errorf("profile: marshal audit details: %w", err)
	}
	targetID := userID
	return s.Queries.InsertAuditLog(ctx, gen.InsertAuditLogParams{
		ActorID:    &userID,
		Action:     "identity.linked",
		TargetType: "user",
		TargetID:   &targetID,
		Details:    details,
		CreatedAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
}

// LogKeyRemoval writes the audit_log row for an SSH key removal. The Profile
// screen calls this after a successful DeleteCredentialByID.
func (s *ProfileService) LogKeyRemoval(ctx context.Context, userID int64, fingerprint string) error {
	details, err := json.Marshal(map[string]string{
		"provider":    "Ssh",
		"fingerprint": fingerprint,
	})
	if err != nil {
		return fmt.Errorf("profile: marshal audit details: %w", err)
	}
	targetID := userID
	return s.Queries.InsertAuditLog(ctx, gen.InsertAuditLogParams{
		ActorID:    &userID,
		Action:     "identity.unlinked",
		TargetType: "user",
		TargetID:   &targetID,
		Details:    details,
		CreatedAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
}

// BatchHasPfp resolves a set of handles to a "has uploaded a profile picture"
// map keyed by lowercased handle. Used by the chat screen on bootstrap so the
// "●" marker is correct on first paint instead of trickling in lazily.
//
// Handles missing from the result map (because the user row no longer exists)
// should default to false at the call site.
func (s *ProfileService) BatchHasPfp(ctx context.Context, handles []string) (map[string]bool, error) {
	if len(handles) == 0 {
		return map[string]bool{}, nil
	}
	rows, err := s.Queries.BatchHasPfpByHandles(ctx, handles)
	if err != nil {
		return nil, fmt.Errorf("profile: batch has pfp: %w", err)
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		// sqlc maps `IS NOT NULL` to interface{} because it doesn't carry
		// the bool through the cast. Defensive type-assert.
		switch v := r.HasPfp.(type) {
		case bool:
			out[strings.ToLower(r.Handle)] = v
		case *bool:
			if v != nil {
				out[strings.ToLower(r.Handle)] = *v
			}
		}
	}
	return out, nil
}
