-- name: InsertMultiplayerHand :one
-- Parent row for one settled hand at one multiplayer table. The
-- (game_key, table_id, hand_no) unique index means re-settling the same
-- hand would error — callers must guarantee hand_no monotonically
-- increases per (game_key, table_id).
INSERT INTO multiplayer_hands (game_key, table_id, hand_no, details, settled_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: InsertGameRoundWithHand :exec
-- Multiplayer variant of InsertGameRound: one row per human seat at hand-
-- end, with hand_id FK back to the multiplayer_hands parent row. The
-- heavy per-hand JSON lives once on the parent; child rows carry only the
-- economic outcome (bet/payout/net). Keep this signature parallel to
-- InsertGameRound so the WalletService can read either flavor uniformly.
INSERT INTO game_rounds (user_id, game_key, bet, payout, net, details, played_at, hand_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);
