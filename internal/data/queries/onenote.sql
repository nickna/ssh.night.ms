-- OneNote thin-cache queries. See migration 000012_onenote. All owner-scoped
-- by user_id; Graph object ids are opaque text.

-- name: GetOneNotePrefs :one
SELECT user_id, default_notebook_id, default_section_id,
       last_synced_at, created_at, updated_at
FROM onenote_user_prefs
WHERE user_id = $1;

-- name: UpsertOneNotePrefs :exec
-- Sets the default notebook/section + last_synced_at. created_at is only set
-- on first insert ($5); updated_at ($6) advances on every write.
INSERT INTO onenote_user_prefs (
    user_id, default_notebook_id, default_section_id,
    last_synced_at, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6
)
ON CONFLICT (user_id) DO UPDATE SET
    default_notebook_id = EXCLUDED.default_notebook_id,
    default_section_id  = EXCLUDED.default_section_id,
    last_synced_at      = EXCLUDED.last_synced_at,
    updated_at          = EXCLUDED.updated_at;

-- name: UpsertOneNoteRecentPage :exec
-- Records (or bumps) a recently-viewed page. ON CONFLICT keeps one row per
-- (user, page) and refreshes the display metadata + last_viewed_at so the
-- "jump back in" list re-sorts to the top.
INSERT INTO onenote_recent_pages (
    user_id, page_id, section_id, title, web_url,
    page_modified_at, last_viewed_at, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
ON CONFLICT (user_id, page_id) DO UPDATE SET
    section_id       = EXCLUDED.section_id,
    title            = EXCLUDED.title,
    web_url          = EXCLUDED.web_url,
    page_modified_at = EXCLUDED.page_modified_at,
    last_viewed_at   = EXCLUDED.last_viewed_at;

-- name: ListOneNoteRecentPages :many
SELECT id, user_id, page_id, section_id, title, web_url,
       page_modified_at, last_viewed_at, created_at
FROM onenote_recent_pages
WHERE user_id = $1
ORDER BY last_viewed_at DESC
LIMIT $2;

-- name: DeleteOneNoteRecentPage :exec
DELETE FROM onenote_recent_pages
WHERE user_id = $1 AND page_id = $2;

-- name: PruneOneNoteRecentPages :exec
-- Caps the recent list at the newest $2 rows per user — called after an
-- upsert so the table can't grow without bound. Outer table aliased so the
-- correlated user_id reference is unambiguous.
DELETE FROM onenote_recent_pages r
WHERE r.user_id = $1
  AND r.id NOT IN (
      SELECT id FROM onenote_recent_pages
      WHERE user_id = $1
      ORDER BY last_viewed_at DESC
      LIMIT $2
  );

-- name: GetOneNotePageCache :one
SELECT user_id, page_id, html, blocks, page_modified_at, fetched_at
FROM onenote_page_cache
WHERE user_id = $1 AND page_id = $2;

-- name: UpsertOneNotePageCache :exec
INSERT INTO onenote_page_cache (
    user_id, page_id, html, blocks, page_modified_at, fetched_at
) VALUES (
    $1, $2, $3, $4, $5, $6
)
ON CONFLICT (user_id, page_id) DO UPDATE SET
    html             = EXCLUDED.html,
    blocks           = EXCLUDED.blocks,
    page_modified_at = EXCLUDED.page_modified_at,
    fetched_at       = EXCLUDED.fetched_at;

-- name: DeleteOneNotePageCache :exec
DELETE FROM onenote_page_cache
WHERE user_id = $1 AND page_id = $2;
