-- Persisted roulette table snapshot. The BBS hosts exactly one global
-- roulette table, so the row is keyed by name ('global') instead of a
-- serial id. JSON snapshot mirrors the holdem_tables design — narrow
-- schema, all state in the blob — so engine changes don't churn the
-- migrations. Cross-stack note: this table is Go-side-only; the .NET
-- predecessor has no roulette feature.

CREATE TABLE IF NOT EXISTS roulette_state (
    name        TEXT PRIMARY KEY,
    snapshot    JSONB NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
