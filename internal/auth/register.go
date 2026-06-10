package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickna/ssh.night.ms/internal/settings"
)

// RegistrationErr is the typed error returned from CreateAccount. The screen
// uses the Kind to pick a user-facing message; the wrapped error retains the
// driver-level detail for logs.
type RegistrationErr struct {
	Kind RegistrationErrKind
	Err  error
}

func (e *RegistrationErr) Error() string {
	if e.Err == nil {
		return string(e.Kind)
	}
	return string(e.Kind) + ": " + e.Err.Error()
}

func (e *RegistrationErr) Unwrap() error { return e.Err }

type RegistrationErrKind string

const (
	RegErrHandleInvalid    RegistrationErrKind = "handle-invalid"
	RegErrPasswordTooShort RegistrationErrKind = "password-too-short"
	RegErrHandleTaken      RegistrationErrKind = "handle-taken"
	RegErrKeyAlreadyUsed   RegistrationErrKind = "key-already-used"
	RegErrSignupsDisabled  RegistrationErrKind = "signups-disabled"
	RegErrInternal         RegistrationErrKind = "internal"
)

// RegisterInput carries the form values + the optional adoptable SSH key the
// client offered at handshake time.
type RegisterInput struct {
	Handle   string
	Password string

	// AdoptKey + Offered* are only honored when AdoptKey is true AND the offered
	// triplet is non-empty. Otherwise the user is created password-only.
	AdoptKey           bool
	OfferedFingerprint string
	OfferedAlgorithm   string
	OfferedBlob        []byte
}

// RegisterDeps bundles the singletons CreateAccount needs. Lives separately
// from auth.Lookup because the register flow doesn't touch the rate limiter
// (signup is not an authentication attempt).
type RegisterDeps struct {
	Pool                 *pgxpool.Pool
	Hasher               *Hasher
	BootstrapSysopHandle string // matched case-insensitively; empty disables auto-promotion
	MinPasswordLength    int

	// Settings is the runtime-tunable cache consulted for the signups-enabled
	// kill switch. Nil-safe: when omitted (tests), signups are always allowed.
	Settings *settings.Cache
}

// CreateAccount inserts a users row + optional identity_credentials row in one
// transaction. Returns the resulting Known principal — screens hand that
// straight to session.Session.Identity and pick up where SignupRequired left
// off.
func CreateAccount(ctx context.Context, deps RegisterDeps, in RegisterInput) (Known, error) {
	// Signups kill switch — checked before any validation work so a closed
	// gate short-circuits cheaply. The screen translates this to the sysop-
	// configured signups_disabled_message.
	if deps.Settings != nil && !deps.Settings.Get().SignupsEnabled {
		return Known{}, &RegistrationErr{Kind: RegErrSignupsDisabled}
	}
	handle := strings.TrimSpace(in.Handle)
	if !IsValidHandle(handle) {
		return Known{}, &RegistrationErr{Kind: RegErrHandleInvalid}
	}
	minLen := deps.MinPasswordLength
	if minLen <= 0 {
		minLen = 8
	}
	if len(in.Password) < minLen {
		return Known{}, &RegistrationErr{Kind: RegErrPasswordTooShort}
	}

	hash, algo, err := deps.Hasher.Hash(in.Password)
	if err != nil {
		return Known{}, &RegistrationErr{Kind: RegErrInternal, Err: err}
	}
	var algoPtr *string
	if algo != "" {
		algoPtr = &algo
	}

	now := time.Now().UTC()
	promoteSysop := deps.BootstrapSysopHandle != "" &&
		strings.EqualFold(deps.BootstrapSysopHandle, handle)
	adoptKey := in.AdoptKey && in.OfferedFingerprint != "" && len(in.OfferedBlob) > 0

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Known{}, &RegistrationErr{Kind: RegErrInternal, Err: err}
	}
	defer tx.Rollback(ctx)

	var userID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO users (
			handle, created_at, last_seen_at, is_sysop, is_banned,
			clock_format, date_format, temperature_unit, time_zone_id, location_source,
			password_hash, password_algo, password_updated_at,
			suppress_key_adoption_prompts, require_ssh_key
		) VALUES (
			$1, $2, $2, $3, FALSE,
			0, 0, 0, 'UTC', 0,
			$4, $5, $2,
			FALSE, FALSE
		) RETURNING id`,
		handle, now, promoteSysop, hash, algoPtr,
	).Scan(&userID); err != nil {
		return Known{}, classifyInsertError(err)
	}

	if adoptKey {
		metadata, mErr := json.Marshal(map[string]any{
			"algorithm": in.OfferedAlgorithm,
			"blob_b64":  base64.StdEncoding.EncodeToString(in.OfferedBlob),
		})
		if mErr != nil {
			return Known{}, &RegistrationErr{Kind: RegErrInternal, Err: mErr}
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO identity_credentials (
				user_id, provider, subject, metadata, label, created_at, last_used_at
			) VALUES ($1, $2, $3, $4, $5, $6, $6)`,
			userID, CredentialProviderSSH, in.OfferedFingerprint, metadata, "adopted at signup", now,
		); err != nil {
			return Known{}, classifyInsertError(err)
		}
	}

	auditDetails := map[string]any{
		"handle":    handle,
		"adopt_key": adoptKey,
		"via":       "register-screen",
	}
	if promoteSysop {
		auditDetails["sysop_bootstrap"] = true
	}
	if err := insertAuditLog(ctx, tx, nil, "user.registered", "user", &userID, auditDetails, now); err != nil {
		return Known{}, &RegistrationErr{Kind: RegErrInternal, Err: err}
	}

	if err := tx.Commit(ctx); err != nil {
		return Known{}, &RegistrationErr{Kind: RegErrInternal, Err: err}
	}

	return Known{
		UserID:  userID,
		Handle:  handle,
		IsSysop: promoteSysop,
	}, nil
}

// IsValidHandle accepts 3-32 chars of ASCII letters, digits, underscore, or
// dash. Exported so web-layer handlers (rename, OAuth linking) can validate
// without duplicating the rule.
func IsValidHandle(handle string) bool {
	if len(handle) < 3 || len(handle) > 32 {
		return false
	}
	for _, r := range handle {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// classifyInsertError translates a Postgres unique_violation (SQLSTATE 23505)
// into the right RegistrationErrKind so the screen can pick a precise message.
// Other errors are wrapped as RegErrInternal.
func classifyInsertError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "ix_users_handle":
			return &RegistrationErr{Kind: RegErrHandleTaken, Err: err}
		case "ix_identity_credentials_provider_subject":
			return &RegistrationErr{Kind: RegErrKeyAlreadyUsed, Err: err}
		}
	}
	return &RegistrationErr{Kind: RegErrInternal, Err: err}
}
