-- name: LeaderboardTopSingleWins :many
-- Biggest single-hand net wins across all door games. The .NET stack relies
-- on a partial-index-like access pattern (game_rounds.net > 0 sorted desc);
-- without it, this scan would also dredge up the biggest losses at the
-- bottom of the same index, which we don't want here.
--
-- Pre-cutover holdem-mp rows used to record one row per buy-in/cashout
-- session (entire window as a single Bet/Payout pair) — the per-hand
-- settlement commit changed that, but legacy rows linger with hand_id
-- NULL. Filter them out so a multi-hand session total doesn't masquerade
-- as a single hand on this view. Single-player games (slots / videopoker /
-- blackjack / single-player holdem) also have hand_id NULL by design;
-- those stay in scope because each of THEIR rows really is one round.
SELECT r.net, r.game_key, r.played_at, u.handle
FROM game_rounds r
JOIN users u ON u.id = r.user_id
WHERE r.net > 0
  AND NOT (r.game_key = 'holdem-mp' AND r.hand_id IS NULL)
ORDER BY r.net DESC, r.id DESC
LIMIT $1;

-- name: LeaderboardLifetimeNet :many
-- Cumulative net coins per user across all games, top N. Negative totals
-- still rank — a user grinding through a losing streak shows up at the
-- bottom, which is intentional ("worst grinder" is a real leaderboard
-- shape on this stack).
SELECT u.handle, SUM(r.net)::bigint AS total_net
FROM game_rounds r
JOIN users u ON u.id = r.user_id
GROUP BY u.id, u.handle
ORDER BY total_net DESC
LIMIT $1;

-- name: LeaderboardHotStreaks :many
-- Cumulative net coins per user over the last N days. The cutoff is passed
-- in as a Timestamptz rather than computed inline so the service can pin
-- a single "now" across the request and reuse it elsewhere if needed.
SELECT u.handle, SUM(r.net)::bigint AS total_net
FROM game_rounds r
JOIN users u ON u.id = r.user_id
WHERE r.played_at > $1
GROUP BY u.id, u.handle
ORDER BY total_net DESC
LIMIT $2;
