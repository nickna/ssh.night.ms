-- Persisted Hold'em table snapshots so multiplayer tables survive a
-- graceful server restart. The schema is intentionally narrow — name +
-- blinds + JSON blob — because the in-memory Game has lots of fields
-- and we'd churn this table every release if we normalized seats.

CREATE TABLE IF NOT EXISTS holdem_tables (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    cap_seats    INTEGER NOT NULL CHECK (cap_seats BETWEEN 2 AND 9),
    small_blind  INTEGER NOT NULL,
    big_blind    INTEGER NOT NULL,
    snapshot     JSONB NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ix_holdem_tables_updated_at
    ON holdem_tables (updated_at DESC);
