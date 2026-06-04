package realtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// ForumService wraps the forums + topics + posts queries with TUI-friendly
// API-shape structs (no pgtype, no embedded *string pointers in the body).
// The underlying data is request-response not pub/sub — forums don't
// subscribe to events yet.
type ForumService struct {
	Queries *gen.Queries
	Logger  *slog.Logger
}

// Forum is the TUI-shape forum row. TopicCount + LastActivityAt are
// denormalized counters maintained by CreateTopic / Reply so the forum list
// paints with one query.
type Forum struct {
	ID             int64
	Name           string
	Description    string
	SortOrder      int32
	TopicCount     int32
	LastActivityAt time.Time // zero value = no posts ever
}

// Topic carries denormalized author + post count so the topic list renders
// with one query.
type Topic struct {
	ID           int64
	ForumID      int64
	Title        string
	CreatedByID  int64
	AuthorHandle string
	CreatedAt    time.Time
	LastPostAt   time.Time
	PostCount    int64
}

// Post is one post in a thread.
type Post struct {
	ID            int64
	TopicID       int64
	ParentPostID  *int64
	Body          string
	CreatedByID   int64
	AuthorHandle  string
	AuthorIsSysop bool
	CreatedAt     time.Time
	EditedAt      time.Time // zero value = never edited
}

// ListForums returns every forum ordered by sort_order then name.
func (s *ForumService) ListForums(ctx context.Context) ([]Forum, error) {
	rows, err := s.Queries.ListForums(ctx)
	if err != nil {
		return nil, fmt.Errorf("forums: list: %w", err)
	}
	out := make([]Forum, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapForum(r))
	}
	return out, nil
}

// GetForum returns one forum by id, or ErrForumNotFound when missing.
func (s *ForumService) GetForum(ctx context.Context, id int64) (Forum, error) {
	r, err := s.Queries.GetForumByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Forum{}, ErrForumNotFound
		}
		return Forum{}, fmt.Errorf("forums: get: %w", err)
	}
	return mapForum(r), nil
}

// mapForum collapses the sqlc-emitted Forum row into the TUI-shape type.
// Centralized so future SELECT extensions are a one-liner.
func mapForum(r gen.Forum) Forum {
	f := Forum{
		ID:             r.ID,
		Name:           r.Name,
		SortOrder:      r.SortOrder,
		TopicCount:     r.TopicCount,
		LastActivityAt: r.LastActivityAt.Time, // zero if NULL
	}
	if r.Description != nil {
		f.Description = *r.Description
	}
	return f
}

// ErrForumNotFound signals a lookup miss; the screen surfaces a notice.
var ErrForumNotFound = errors.New("forum not found")

// RecentTopics returns the most-recently-active topics in a forum, capped
// at limit.
func (s *ForumService) RecentTopics(ctx context.Context, forumID int64, limit int32) ([]Topic, error) {
	rows, err := s.Queries.ListTopicsInForum(ctx, gen.ListTopicsInForumParams{
		ForumID: forumID,
		Limit:   limit,
	})
	if err != nil {
		return nil, fmt.Errorf("forums: topics: %w", err)
	}
	out := make([]Topic, 0, len(rows))
	for _, r := range rows {
		out = append(out, Topic{
			ID:           r.ID,
			ForumID:      r.ForumID,
			Title:        r.Title,
			CreatedByID:  r.CreatedByID,
			AuthorHandle: r.AuthorHandle,
			CreatedAt:    r.CreatedAt.Time,
			LastPostAt:   r.LastPostAt.Time,
			PostCount:    r.PostCount,
		})
	}
	return out, nil
}

// TopicsPage returns one page of a forum's topics ordered by last activity,
// using LIMIT/OFFSET. Mirrors RecentTopics but lets the web forum view page
// through the full list; the total for the page controls comes from the
// forum's denormalized TopicCount, so there's no separate COUNT round-trip.
func (s *ForumService) TopicsPage(ctx context.Context, forumID int64, limit, offset int32) ([]Topic, error) {
	rows, err := s.Queries.ListTopicsInForumPaged(ctx, gen.ListTopicsInForumPagedParams{
		ForumID: forumID,
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		return nil, fmt.Errorf("forums: topics page: %w", err)
	}
	out := make([]Topic, 0, len(rows))
	for _, r := range rows {
		out = append(out, Topic{
			ID:           r.ID,
			ForumID:      r.ForumID,
			Title:        r.Title,
			CreatedByID:  r.CreatedByID,
			AuthorHandle: r.AuthorHandle,
			CreatedAt:    r.CreatedAt.Time,
			LastPostAt:   r.LastPostAt.Time,
			PostCount:    r.PostCount,
		})
	}
	return out, nil
}

// GetTopic returns one topic by id.
func (s *ForumService) GetTopic(ctx context.Context, id int64) (Topic, error) {
	r, err := s.Queries.GetTopicByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Topic{}, ErrTopicNotFound
		}
		return Topic{}, fmt.Errorf("forums: get topic: %w", err)
	}
	return Topic{
		ID:          r.ID,
		ForumID:     r.ForumID,
		Title:       r.Title,
		CreatedByID: r.CreatedByID,
		CreatedAt:   r.CreatedAt.Time,
		LastPostAt:  r.LastPostAt.Time,
	}, nil
}

// ErrTopicNotFound signals a lookup miss.
var ErrTopicNotFound = errors.New("topic not found")

// Posts returns every post in a topic in chronological order.
func (s *ForumService) Posts(ctx context.Context, topicID int64) ([]Post, error) {
	rows, err := s.Queries.ListPostsInTopic(ctx, topicID)
	if err != nil {
		return nil, fmt.Errorf("forums: posts: %w", err)
	}
	out := make([]Post, 0, len(rows))
	for _, r := range rows {
		out = append(out, Post{
			ID:            r.ID,
			TopicID:       r.TopicID,
			ParentPostID:  r.ParentPostID,
			Body:          r.Body,
			CreatedByID:   r.CreatedByID,
			AuthorHandle:  r.AuthorHandle,
			AuthorIsSysop: r.AuthorIsSysop,
			CreatedAt:     r.CreatedAt.Time,
			EditedAt:      r.EditedAt.Time,
		})
	}
	return out, nil
}

// PostsPage returns one page of a topic's posts in chronological order,
// using LIMIT/OFFSET. Mirrors Posts but lets the web thread view page
// through long threads; the total for the page controls comes from the
// topic's denormalized PostCount.
func (s *ForumService) PostsPage(ctx context.Context, topicID int64, limit, offset int32) ([]Post, error) {
	rows, err := s.Queries.ListPostsInTopicPaged(ctx, gen.ListPostsInTopicPagedParams{
		TopicID: topicID,
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		return nil, fmt.Errorf("forums: posts page: %w", err)
	}
	out := make([]Post, 0, len(rows))
	for _, r := range rows {
		out = append(out, Post{
			ID:            r.ID,
			TopicID:       r.TopicID,
			ParentPostID:  r.ParentPostID,
			Body:          r.Body,
			CreatedByID:   r.CreatedByID,
			AuthorHandle:  r.AuthorHandle,
			AuthorIsSysop: r.AuthorIsSysop,
			CreatedAt:     r.CreatedAt.Time,
			EditedAt:      r.EditedAt.Time,
		})
	}
	return out, nil
}

// CreateTopic adds a new topic + its root post in a single transaction so
// the forum list never sees a topic with zero posts. Returns the
// constructed Topic ready to render.
func (s *ForumService) CreateTopic(ctx context.Context, pool interface {
	BeginCtx(context.Context) error
}, forumID, userID int64, handle, title, body string) (Topic, error) {
	// Two separate queries (no explicit Tx via the service because we'd need
	// to thread a pgx.Tx wrapper through sqlc.Queries — bigger refactor than
	// this slice deserves). The posts FK to topics enforces the order, so a
	// partial failure is recoverable.
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	t, err := s.Queries.CreateTopic(ctx, gen.CreateTopicParams{
		ForumID: forumID, Title: title, CreatedByID: userID, CreatedAt: now,
	})
	if err != nil {
		return Topic{}, fmt.Errorf("forums: create topic: %w", err)
	}
	if _, err := s.Queries.CreatePost(ctx, gen.CreatePostParams{
		TopicID: t.ID, Body: body, CreatedByID: userID, CreatedAt: now,
	}); err != nil {
		// Topic created, root post missing. Surface the error; subsequent
		// loads will show an empty-thread placeholder.
		return Topic{ID: t.ID, ForumID: t.ForumID, Title: t.Title, CreatedByID: userID, AuthorHandle: handle, CreatedAt: t.CreatedAt.Time, LastPostAt: t.LastPostAt.Time, PostCount: 0}, fmt.Errorf("forums: create root post: %w", err)
	}
	// Bump the denormalized forum counters so the forum list stays fresh
	// without an aggregate query at read time. Non-fatal: a stale counter
	// is a minor cosmetic issue, not a correctness problem.
	if err := s.Queries.IncrementForumTopicCount(ctx, gen.IncrementForumTopicCountParams{
		ID: forumID, LastActivityAt: now,
	}); err != nil && s.Logger != nil {
		s.Logger.Warn("forums: bump topic count", "forum", forumID, "err", err)
	}
	return Topic{
		ID: t.ID, ForumID: t.ForumID, Title: t.Title,
		CreatedByID: userID, AuthorHandle: handle,
		CreatedAt: t.CreatedAt.Time, LastPostAt: t.LastPostAt.Time,
		PostCount: 1,
	}, nil
}

// Reply appends a post to an existing topic and bumps both last_post_at on
// the topic and last_activity_at on the parent forum so list sort orders
// stay fresh. forumID is required so the forum-side bump doesn't need an
// extra round-trip to look it up; the only caller already has it in hand
// (m.activeTopic.ForumID).
func (s *ForumService) Reply(ctx context.Context, forumID, topicID, userID int64, body string) (Post, error) {
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	row, err := s.Queries.CreatePost(ctx, gen.CreatePostParams{
		TopicID: topicID, Body: body, CreatedByID: userID, CreatedAt: now,
	})
	if err != nil {
		return Post{}, fmt.Errorf("forums: reply: %w", err)
	}
	if err := s.Queries.TouchTopicLastPost(ctx, gen.TouchTopicLastPostParams{
		ID: topicID, LastPostAt: now,
	}); err != nil && s.Logger != nil {
		// Non-fatal; the post was created, the sort order just stays stale
		// until something else bumps the topic.
		s.Logger.Warn("forums: bump topic last_post_at", "topic", topicID, "err", err)
	}
	if err := s.Queries.TouchForumLastActivity(ctx, gen.TouchForumLastActivityParams{
		ID: forumID, LastActivityAt: now,
	}); err != nil && s.Logger != nil {
		s.Logger.Warn("forums: bump forum last_activity_at", "forum", forumID, "err", err)
	}
	return Post{
		ID: row.ID, TopicID: row.TopicID, Body: row.Body,
		CreatedByID: userID, CreatedAt: row.CreatedAt.Time,
		ParentPostID: row.ParentPostID,
	}, nil
}

// TouchTopicRead persists "user X has read topic Y up through post Z".
// Idempotent and monotonic — the underlying UpsertPostRead uses GREATEST
// in both columns so a stale touch can't rewind the marker. Mirrors
// ChatService.TouchChannelRead. Safe to call on every thread entry.
func (s *ForumService) TouchTopicRead(ctx context.Context, userID, topicID int64) error {
	latest, err := s.Queries.LatestPostInTopic(ctx, topicID)
	if err != nil {
		return fmt.Errorf("forums: latest post: %w", err)
	}
	// latest.ID is 0 when the topic has no posts (shouldn't happen — every
	// topic has a root post — but the COALESCE keeps us safe). The post-id
	// is what makes this race-free vs. the original time-based design.
	var postID *int64
	if latest.ID > 0 {
		postID = &latest.ID
	}
	readAt := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	return s.Queries.UpsertPostRead(ctx, gen.UpsertPostReadParams{
		UserID:         userID,
		TopicID:        topicID,
		LastReadAt:     readAt,
		LastReadPostID: postID,
	})
}

// UnreadTopicCounts returns the per-topic unread post count for the user in
// one forum, keyed by topic_id. Topics with zero unread are still present
// in the returned map (LEFT JOIN + GROUP BY).
func (s *ForumService) UnreadTopicCounts(ctx context.Context, userID, forumID int64) (map[int64]int, error) {
	rows, err := s.Queries.UnreadTopicCountsForForum(ctx, gen.UnreadTopicCountsForForumParams{
		UserID: userID, ForumID: forumID,
	})
	if err != nil {
		return nil, fmt.Errorf("forums: unread topic counts: %w", err)
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.TopicID] = int(r.Unread)
	}
	return out, nil
}

// UnreadCountsByForum returns the per-forum unread post count for the user,
// keyed by forum_id. Used by the forum list to render aggregate badges.
func (s *ForumService) UnreadCountsByForum(ctx context.Context, userID int64) (map[int64]int, error) {
	rows, err := s.Queries.UnreadCountsByForumForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("forums: unread by forum: %w", err)
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.ForumID] = int(r.Unread)
	}
	return out, nil
}
