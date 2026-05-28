package realtime

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// SavedLocation is the user-facing view of one row in user_saved_locations.
// Lives in realtime/ next to ProfileService because both wrap per-user
// preferences read from the same pool.
type SavedLocation struct {
	ID        int64
	Label     string
	Latitude  float64
	Longitude float64
	Canonical string // empty if the row's canonical column was NULL
	SortOrder int32
}

// ErrLocationDuplicateLabel is returned when AddLocation hits the unique
// (user_id, label) index. The Profile screen surfaces this as a friendly
// "label already in use" message.
var ErrLocationDuplicateLabel = errors.New("location label already in use")

// MaxSavedLocationsPerUser caps how many rows a single user can hold. Kept
// at 12 to keep the carousel-style picker (when it lands) small enough to
// fit on a single screen.
const MaxSavedLocationsPerUser = 12

// ErrLocationLimitReached is returned by AddLocation when the user is
// already at MaxSavedLocationsPerUser. Caller should hint that the user
// delete one before adding another.
var ErrLocationLimitReached = errors.New("location limit reached")

// LocationService is the back end of the Profile-screen Locations tab.
// Methods are intentionally narrow — list + add + delete + primary — so
// the surface stays auditable. Rename + reorder are deliberate omissions
// for now and remain a follow-up.
type LocationService struct {
	Queries *gen.Queries
}

// List returns the user's locations ordered by sort_order ASC, id ASC. An
// empty result is not an error.
func (s *LocationService) List(ctx context.Context, userID int64) ([]SavedLocation, error) {
	rows, err := s.Queries.ListUserSavedLocations(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("locations: list: %w", err)
	}
	out := make([]SavedLocation, 0, len(rows))
	for _, r := range rows {
		out = append(out, locationFromRow(r))
	}
	return out, nil
}

// GetPrimary returns the row with the lowest sort_order, or nil when the
// user has nothing saved (so callers can fall back to defaults without
// branching on errors.Is(err, pgx.ErrNoRows)).
func (s *LocationService) GetPrimary(ctx context.Context, userID int64) (*SavedLocation, error) {
	row, err := s.Queries.GetPrimaryUserSavedLocation(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("locations: get primary: %w", err)
	}
	loc := locationFromRow(row)
	return &loc, nil
}

// Add inserts a new row at the tail of the user's list. Latitude/longitude
// are validated against the standard WGS84 bounds; the label is trimmed and
// bounded by the column's 64-char limit. canonical is the disambiguating
// "Name, Admin1, Country" string from the geocoder — empty when the user
// typed coords manually. Returns ErrLocationDuplicateLabel when the unique
// (user_id, label) index fires.
func (s *LocationService) Add(ctx context.Context, userID int64, label, canonical string, lat, lon float64) (SavedLocation, error) {
	label = strings.TrimSpace(label)
	canonical = strings.TrimSpace(canonical)
	if label == "" {
		return SavedLocation{}, errors.New("locations: label is required")
	}
	if len(label) > 64 {
		return SavedLocation{}, errors.New("locations: label too long (max 64 chars)")
	}
	if len(canonical) > 160 {
		// Column's varchar(160) cap. Long geocoder results are pathological
		// (admin1+country padding inflated by a translation quirk); trim
		// rather than fail so the row still saves.
		canonical = canonical[:160]
	}
	if lat < -90 || lat > 90 {
		return SavedLocation{}, fmt.Errorf("locations: latitude %v out of range [-90, 90]", lat)
	}
	if lon < -180 || lon > 180 {
		return SavedLocation{}, fmt.Errorf("locations: longitude %v out of range [-180, 180]", lon)
	}
	// Check the cap before issuing the insert so we return the friendlier
	// error rather than a bare unique-constraint violation later.
	existing, err := s.Queries.ListUserSavedLocations(ctx, userID)
	if err != nil {
		return SavedLocation{}, fmt.Errorf("locations: precheck: %w", err)
	}
	if len(existing) >= MaxSavedLocationsPerUser {
		return SavedLocation{}, ErrLocationLimitReached
	}
	next, err := s.Queries.NextUserSavedLocationSortOrder(ctx, userID)
	if err != nil {
		return SavedLocation{}, fmt.Errorf("locations: next sort: %w", err)
	}
	var canonicalPtr *string
	if canonical != "" {
		canonicalPtr = &canonical
	}
	row, err := s.Queries.AddUserSavedLocation(ctx, gen.AddUserSavedLocationParams{
		UserID:    userID,
		Label:     label,
		Latitude:  lat,
		Longitude: lon,
		Canonical: canonicalPtr,
		SortOrder: next,
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return SavedLocation{}, ErrLocationDuplicateLabel
		}
		return SavedLocation{}, fmt.Errorf("locations: add: %w", err)
	}
	// When this insert landed as the only row, it is also the new primary —
	// mirror its label to users.location so legacy readers stay correct.
	// Otherwise the mirror is owned by whichever existing row sits at the
	// lowest sort_order and is left alone here.
	if len(existing) == 0 {
		mirror := row.Label
		if err := s.Queries.SetUserLocationLabel(ctx, gen.SetUserLocationLabelParams{ID: userID, Location: &mirror}); err != nil {
			return locationFromRow(row), fmt.Errorf("locations: mirror users.location: %w", err)
		}
	}
	return locationFromRow(row), nil
}

// SetPrimaryFromGeocode promotes (or inserts) a row matching the given
// coordinates so that it becomes the user's primary saved location. The
// rules:
//
//   - If an existing row matches the supplied lat/lon (within ~0.0001°, ~11 m),
//     it is promoted by setting its sort_order one less than the current
//     minimum — pushing it ahead of every other row without disturbing the
//     rest of the list. The row's existing label is kept.
//   - Otherwise a new row is inserted at min(sort_order)-1 using the
//     supplied label + canonical. Duplicate-label collisions surface as
//     ErrLocationDuplicateLabel and the cap surfaces as ErrLocationLimitReached
//     (same as Add).
//
// After the primary is settled, users.location is mirrored to the new
// primary's label so legacy readers see the same string they used to.
// Returns the resulting SavedLocation.
//
// The caller is responsible for refreshing Session.PrimaryLocation (via
// RefreshPrimaryLocation) so in-session callers like WeatherCoords() pick
// up the change without a re-login.
func (s *LocationService) SetPrimaryFromGeocode(ctx context.Context, userID int64, label, canonical string, lat, lon float64) (SavedLocation, error) {
	label = strings.TrimSpace(label)
	canonical = strings.TrimSpace(canonical)
	if label == "" {
		return SavedLocation{}, errors.New("locations: label is required")
	}
	if len(label) > 64 {
		return SavedLocation{}, errors.New("locations: label too long (max 64 chars)")
	}
	if lat < -90 || lat > 90 {
		return SavedLocation{}, fmt.Errorf("locations: latitude %v out of range [-90, 90]", lat)
	}
	if lon < -180 || lon > 180 {
		return SavedLocation{}, fmt.Errorf("locations: longitude %v out of range [-180, 180]", lon)
	}
	if len(canonical) > 160 {
		canonical = canonical[:160]
	}

	existing, err := s.Queries.ListUserSavedLocations(ctx, userID)
	if err != nil {
		return SavedLocation{}, fmt.Errorf("locations: precheck: %w", err)
	}

	// 0.0001° (~11 m) is finer than any city centroid the geocoder returns,
	// coarser than float-roundtrip noise. Comfortable in the gap between
	// "same place" and "different place" for the practical inputs.
	const coordEpsilon = 1e-4

	matchIdx := -1
	for i, row := range existing {
		if math.Abs(row.Latitude-lat) < coordEpsilon && math.Abs(row.Longitude-lon) < coordEpsilon {
			matchIdx = i
			break
		}
	}

	headSort := int32(0)
	if len(existing) > 0 {
		headSort = existing[0].SortOrder - 1
	}

	var result SavedLocation
	switch {
	case matchIdx == 0:
		// Already the primary at lowest sort_order — leave it alone.
		result = locationFromRow(existing[0])
	case matchIdx > 0:
		// Promote the existing row.
		if err := s.Queries.SetUserSavedLocationSortOrder(ctx, gen.SetUserSavedLocationSortOrderParams{
			ID: existing[matchIdx].ID, UserID: userID, SortOrder: headSort,
		}); err != nil {
			return SavedLocation{}, fmt.Errorf("locations: promote: %w", err)
		}
		result = locationFromRow(existing[matchIdx])
		result.SortOrder = headSort
	default:
		if len(existing) >= MaxSavedLocationsPerUser {
			return SavedLocation{}, ErrLocationLimitReached
		}
		var canonicalPtr *string
		if canonical != "" {
			canonicalPtr = &canonical
		}
		row, err := s.Queries.AddUserSavedLocation(ctx, gen.AddUserSavedLocationParams{
			UserID:    userID,
			Label:     label,
			Latitude:  lat,
			Longitude: lon,
			Canonical: canonicalPtr,
			SortOrder: headSort,
			CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return SavedLocation{}, ErrLocationDuplicateLabel
			}
			return SavedLocation{}, fmt.Errorf("locations: add primary: %w", err)
		}
		result = locationFromRow(row)
	}

	mirror := result.Label
	if err := s.Queries.SetUserLocationLabel(ctx, gen.SetUserLocationLabelParams{
		ID: userID, Location: &mirror,
	}); err != nil {
		return result, fmt.Errorf("locations: mirror users.location: %w", err)
	}
	return result, nil
}

// ClearPrimary removes the user's current primary saved-location row. The
// next-lowest sort_order (if any) is implicitly the new primary; Delete
// re-mirrors users.location accordingly. Returns nil when the user has
// nothing saved (still nulls the mirror, defensive against stale text).
func (s *LocationService) ClearPrimary(ctx context.Context, userID int64) error {
	primary, err := s.GetPrimary(ctx, userID)
	if err != nil {
		return err
	}
	if primary == nil {
		return s.refreshMirror(ctx, userID)
	}
	return s.Delete(ctx, userID, primary.ID)
}

// SeedFromProfile inserts a single saved-location row representing the
// user's legacy profile city (users.location_*). One-shot login-time
// backfill: a fresh List() check inside makes the call a no-op when the
// user already has any saved rows, so a racing concurrent login won't
// double-seed and an explicit user choice is never overwritten. Returns
// (nil, nil) when the list is already non-empty.
func (s *LocationService) SeedFromProfile(ctx context.Context, userID int64, label, canonical string, lat, lon float64) (*SavedLocation, error) {
	existing, err := s.Queries.ListUserSavedLocations(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("locations: seed precheck: %w", err)
	}
	if len(existing) > 0 {
		return nil, nil
	}
	row, err := s.Add(ctx, userID, label, canonical, lat, lon)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Delete removes the row by id. The (id, user_id) match in SQL guards
// against one user deleting another's row. Returns nil even when no row
// matched — the desired end-state is reached either way. The legacy
// users.location mirror is refreshed to whichever row is now primary (or
// NULL when the list is empty) so /finger-style readers stay correct.
func (s *LocationService) Delete(ctx context.Context, userID, id int64) error {
	if err := s.Queries.DeleteUserSavedLocation(ctx, gen.DeleteUserSavedLocationParams{
		ID: id, UserID: userID,
	}); err != nil {
		return fmt.Errorf("locations: delete: %w", err)
	}
	return s.refreshMirror(ctx, userID)
}

// Rename changes a row's label. Same validation as Add (trim, non-empty,
// ≤64 chars). Returns ErrLocationDuplicateLabel on (user_id, label)
// collisions. If the renamed row is the current primary, users.location
// is mirrored to the new label.
func (s *LocationService) Rename(ctx context.Context, userID, id int64, label string) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return errors.New("locations: label is required")
	}
	if len(label) > 64 {
		return errors.New("locations: label too long (max 64 chars)")
	}
	if err := s.Queries.RenameUserSavedLocation(ctx, gen.RenameUserSavedLocationParams{
		ID: id, UserID: userID, Label: label,
	}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrLocationDuplicateLabel
		}
		return fmt.Errorf("locations: rename: %w", err)
	}
	return s.refreshMirror(ctx, userID)
}

// Swap exchanges the sort_order values of two rows. Both rows must
// belong to the user (the (id, user_id) guard in SQL enforces this).
// The (user_id, sort_order) tuple isn't unique, so the transient mid-
// swap state where both rows momentarily share a value is fine. Caller
// passes the row pair from the current ordered list — the Locations
// modal computes ↑/↓ neighbors and hands them in here. If the swap
// changes which row is primary (lowest sort_order), users.location is
// mirrored to the new primary's label.
func (s *LocationService) Swap(ctx context.Context, userID int64, a, b SavedLocation) error {
	if err := s.Queries.SetUserSavedLocationSortOrder(ctx, gen.SetUserSavedLocationSortOrderParams{
		ID: a.ID, UserID: userID, SortOrder: b.SortOrder,
	}); err != nil {
		return fmt.Errorf("locations: swap (a): %w", err)
	}
	if err := s.Queries.SetUserSavedLocationSortOrder(ctx, gen.SetUserSavedLocationSortOrderParams{
		ID: b.ID, UserID: userID, SortOrder: a.SortOrder,
	}); err != nil {
		return fmt.Errorf("locations: swap (b): %w", err)
	}
	return s.refreshMirror(ctx, userID)
}

// refreshMirror writes users.location to whatever row is currently
// primary, or NULL when the user has nothing saved. Cheap (one SELECT +
// one UPDATE) and idempotent; called from every mutation that can change
// the primary so legacy readers stay aligned.
func (s *LocationService) refreshMirror(ctx context.Context, userID int64) error {
	primary, err := s.GetPrimary(ctx, userID)
	if err != nil {
		return err
	}
	var label *string
	if primary != nil {
		l := primary.Label
		label = &l
	}
	if err := s.Queries.SetUserLocationLabel(ctx, gen.SetUserLocationLabelParams{ID: userID, Location: label}); err != nil {
		return fmt.Errorf("locations: mirror users.location: %w", err)
	}
	return nil
}

func locationFromRow(r gen.UserSavedLocation) SavedLocation {
	canonical := ""
	if r.Canonical != nil {
		canonical = *r.Canonical
	}
	return SavedLocation{
		ID:        r.ID,
		Label:     r.Label,
		Latitude:  r.Latitude,
		Longitude: r.Longitude,
		Canonical: canonical,
		SortOrder: r.SortOrder,
	}
}
