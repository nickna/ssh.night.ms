// Package data hosts hand-written query helpers that complement the
// sqlc-generated `gen` package. Use sqlc whenever the query has a fixed
// shape; reach for this package only when the query needs runtime-dynamic
// predicates that sqlc can't model cleanly (currently: the filter-chip
// search on the sysop Events tab).
package data

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// Filter is one parsed filter chip. Exactly one of Text and Time is
// populated; Text-typed dims use Text, time-typed dims (since/until) use Time.
// The Dim field MUST be one of the keys in allowedFilterDims or the filter
// is silently dropped — closes the SQL-injection door even if a caller
// constructs a Filter by hand instead of via the parser.
type Filter struct {
	Dim  string
	Text string
	Time time.Time
}

// allowedFilterDims is the closed set of column-projecting dims the Events
// tab's filter chip parser knows how to translate into WHERE clauses.
// Adding a new dim requires updating both this set AND the buildFilterWhere
// switch below.
var allowedFilterDims = map[string]bool{
	"severity": true,
	"handle":   true,
	"ip":       true,
	"kind":     true,
	"source":   true,
	"since":    true,
	"until":    true,
	"text":     true,
}

// unifiedCTE is the CTE definition shared with the sqlc-generated
// ListUnifiedEvents query. Kept in sync by hand: when the sqlc projection
// in queries/events.sql changes, mirror the change here. A staleness check
// is in events_filtered_test.go (when added).
const unifiedCTE = `
WITH unified AS (
    SELECT
        'audit'::text AS source,
        a.id,
        a.created_at AS at,
        a.action::text AS kind,
        NULL::text AS severity,
        COALESCE(u.handle, '<system>'::citext)::text AS actor,
        NULL::text AS subject_handle,
        NULL::text AS subject_ip,
        (a.target_type::text || CASE WHEN a.target_id IS NULL THEN '' ELSE '#' || a.target_id::text END)::text AS target,
        a.details
    FROM audit_log a
    LEFT JOIN users u ON u.id = a.actor_id
    UNION ALL
    SELECT
        'security'::text AS source,
        s.id,
        s.at,
        s.event_type AS kind,
        s.severity,
        ''::text AS actor,
        s.handle AS subject_handle,
        s.ip_addr AS subject_ip,
        ''::text AS target,
        s.details
    FROM security_events s
)
`

// ListUnifiedEventsFiltered runs the same UNION-ALL projection as the static
// sqlc ListUnifiedEvents but with a dynamic WHERE built from the filter chip
// list. Returns rows in the same shape as gen.ListUnifiedEventsRow so the
// TUI render code is identical for both filtered and unfiltered loads.
//
// `before` is a keyset pagination cursor (pass zero for the first page).
// `limit` is hard-capped at 500 to bound the per-page Postgres + render cost.
func ListUnifiedEventsFiltered(
	ctx context.Context,
	pool *pgxpool.Pool,
	filters []Filter,
	limit int,
	before time.Time,
) ([]gen.ListUnifiedEventsRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	var (
		args     []any
		preds    []string
		argIndex int
	)
	nextArg := func(v any) string {
		args = append(args, v)
		argIndex++
		return fmt.Sprintf("$%d", argIndex)
	}

	if !before.IsZero() {
		preds = append(preds, "at < "+nextArg(before))
	}

	for _, f := range filters {
		if !allowedFilterDims[f.Dim] {
			continue
		}
		switch f.Dim {
		case "severity":
			preds = append(preds, "severity = "+nextArg(f.Text))
		case "source":
			preds = append(preds, "source = "+nextArg(f.Text))
		case "kind":
			preds = append(preds, "kind = "+nextArg(f.Text))
		case "handle":
			ph := nextArg("%" + f.Text + "%")
			preds = append(preds, "(actor ILIKE "+ph+" OR subject_handle ILIKE "+ph+")")
		case "ip":
			// IPs are structured; exact-match avoids matching "1.2.3.4" against
			// "10.1.2.3" the way ILIKE would.
			preds = append(preds, "subject_ip = "+nextArg(f.Text))
		case "text":
			// Free-text falls through to a details::text ILIKE — that catches
			// anything serialised into the jsonb payload plus has the side-
			// effect of matching kind/actor/subject when those happen to be
			// substrings. Good enough for incident triage.
			ph := nextArg("%" + f.Text + "%")
			preds = append(preds, "(details::text ILIKE "+ph+" OR kind ILIKE "+ph+" OR actor ILIKE "+ph+" OR subject_handle ILIKE "+ph+" OR subject_ip ILIKE "+ph+")")
		case "since":
			preds = append(preds, "at >= "+nextArg(f.Time))
		case "until":
			preds = append(preds, "at <= "+nextArg(f.Time))
		}
	}

	var b strings.Builder
	b.WriteString(unifiedCTE)
	b.WriteString("SELECT source, id, at, kind, severity, actor, subject_handle, subject_ip, target, details\nFROM unified\n")
	if len(preds) > 0 {
		b.WriteString("WHERE ")
		b.WriteString(strings.Join(preds, " AND "))
		b.WriteString("\n")
	}
	b.WriteString("ORDER BY at DESC\nLIMIT ")
	b.WriteString(nextArg(int32(limit)))

	rows, err := pool.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("ListUnifiedEventsFiltered: query: %w", err)
	}
	defer rows.Close()

	var out []gen.ListUnifiedEventsRow
	for rows.Next() {
		var r gen.ListUnifiedEventsRow
		// Scan order must match the SELECT column order above.
		if err := rows.Scan(
			&r.Source,
			&r.ID,
			&r.At,
			&r.Kind,
			&r.Severity,
			&r.Actor,
			&r.SubjectHandle,
			&r.SubjectIp,
			&r.Target,
			&r.Details,
		); err != nil {
			return nil, fmt.Errorf("ListUnifiedEventsFiltered: scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListUnifiedEventsFiltered: rows: %w", err)
	}
	return out, nil
}
