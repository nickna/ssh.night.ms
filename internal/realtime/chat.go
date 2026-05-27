package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// ChatService owns chat reads/writes and the bus integration.
type ChatService struct {
	Queries *gen.Queries
	Bus     Bus
	Logger  *slog.Logger
}

// Message is the API-shape chat message used by the TUI. ChatService converts
// the sqlc row types into this so screens never depend on pgtype.
type Message struct {
	ID              int64
	ChannelID       int64
	UserID          int64
	Handle          string
	IsSysop         bool
	Body            string
	CreatedAt       time.Time
	EditedAt        time.Time // zero value = never edited
	DeletedAt       time.Time // zero value = not deleted
	IsPinned        bool      // ★ marker in the renderer
	ParentMessageID *int64    // nil for top-level messages; set for replies
}

// ResolvePublicChannel returns an existing channel by name or creates it if
// missing. Public channels are auto-created on first join (BBS-style).
func (s *ChatService) ResolvePublicChannel(ctx context.Context, name string, createdByID int64) (gen.Channel, error) {
	ch, err := s.Queries.GetChannelByName(ctx, name)
	if err == nil {
		return ch, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return gen.Channel{}, fmt.Errorf("chat: get channel %q: %w", name, err)
	}
	created, err := s.Queries.CreateChannel(ctx, gen.CreateChannelParams{
		Name:        name,
		IsPrivate:   false,
		CreatedByID: &createdByID,
		CreatedAt:   pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		return gen.Channel{}, fmt.Errorf("chat: create channel %q: %w", name, err)
	}
	s.Logger.Info("chat: created public channel", "name", name, "id", created.ID, "by", createdByID)
	return created, nil
}

// RecentMessages returns the latest N messages for a channel, in chronological
// order (oldest first) so the TUI can append them to its log directly.
func (s *ChatService) RecentMessages(ctx context.Context, channelID int64, limit int32) ([]Message, error) {
	rows, err := s.Queries.RecentMessagesForChannel(ctx, gen.RecentMessagesForChannelParams{
		ChannelID: channelID,
		Limit:     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("chat: recent messages: %w", err)
	}
	out := make([]Message, 0, len(rows))
	// rows come newest-first; reverse so the caller can append straight to the log.
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		out = append(out, Message{
			ID:              r.ID,
			ChannelID:       r.ChannelID,
			UserID:          r.UserID,
			Handle:          r.AuthorHandle,
			IsSysop:         r.AuthorIsSysop,
			Body:            r.Body,
			CreatedAt:       r.CreatedAt.Time,
			EditedAt:        r.EditedAt.Time, // pgtype.Timestamptz: zero if NULL
			DeletedAt:       r.DeletedAt.Time,
			IsPinned:        r.IsPinned,
			ParentMessageID: r.ParentMessageID,
		})
	}
	return out, nil
}

// Send writes a new top-level message. Equivalent to SendReply with parentID = nil.
func (s *ChatService) Send(ctx context.Context, channelID int64, author Author, body string) (Message, error) {
	return s.SendReply(ctx, channelID, author, body, nil)
}

// SendReply writes a new message that optionally threads under parentID. The
// published event carries the parent so subscribers can render the indent
// without a follow-up DB hit.
func (s *ChatService) SendReply(ctx context.Context, channelID int64, author Author, body string, parentID *int64) (Message, error) {
	now := time.Now().UTC()
	row, err := s.Queries.InsertChatMessage(ctx, gen.InsertChatMessageParams{
		ChannelID:       channelID,
		UserID:          author.UserID,
		Body:            body,
		CreatedAt:       pgtype.Timestamptz{Time: now, Valid: true},
		ParentMessageID: parentID,
	})
	if err != nil {
		return Message{}, fmt.Errorf("chat: insert message: %w", err)
	}
	msg := Message{
		ID:              row.ID,
		ChannelID:       row.ChannelID,
		UserID:          row.UserID,
		Handle:          author.Handle,
		IsSysop:         author.IsSysop,
		Body:            row.Body,
		CreatedAt:       row.CreatedAt.Time,
		ParentMessageID: row.ParentMessageID,
	}
	payload, err := json.Marshal(ChatEvent{
		Kind:            ChatEventMessageCreated,
		MessageID:       msg.ID,
		ChannelID:       msg.ChannelID,
		UserID:          msg.UserID,
		Handle:          msg.Handle,
		IsSysop:         msg.IsSysop,
		Body:            msg.Body,
		CreatedAt:       msg.CreatedAt,
		ParentMessageID: msg.ParentMessageID,
	})
	if err != nil {
		// We've already persisted; surface the error but the message is
		// durable and will arrive for everyone on next reload.
		return msg, fmt.Errorf("chat: marshal event: %w", err)
	}
	if err := s.Bus.Publish(ctx, T.ChatChannel(channelID), payload); err != nil {
		return msg, fmt.Errorf("chat: publish event: %w", err)
	}
	return msg, nil
}

// LastOwnMessageID returns the user's most recent message id in the channel,
// or 0 if none. Used by /reply to pick what to thread under.
func (s *ChatService) LastOwnMessageID(ctx context.Context, channelID, userID int64) (int64, error) {
	row, err := s.Queries.GetLastOwnMessageInChannel(ctx, gen.GetLastOwnMessageInChannelParams{
		ChannelID: channelID,
		UserID:    userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("chat: last own message: %w", err)
	}
	return row.ID, nil
}

// SubscribeChannel returns a channel of decoded ChatEvent values for the
// given channel ID. The subscription lives as long as ctx is alive.
func (s *ChatService) SubscribeChannel(ctx context.Context, channelID int64) (<-chan ChatEvent, error) {
	raw, err := s.Bus.Subscribe(ctx, T.ChatChannel(channelID))
	if err != nil {
		return nil, err
	}
	out := make(chan ChatEvent, 16)
	go func() {
		defer close(out)
		for payload := range raw {
			var ev ChatEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				s.Logger.Warn("chat: decode event", "err", err, "channel_id", channelID)
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// SubscribeChannels opens one Redis subscription per channel ID and fans
// every event into a single merged stream. The chat screen uses this to
// receive events for every channel the user is in — that's how the sidebar
// can show unread badges without requiring the user to switch in first.
//
// All component subscriptions tear down when ctx cancels; the returned
// channel closes once every component goroutine has drained.
func (s *ChatService) SubscribeChannels(ctx context.Context, channelIDs []int64) (<-chan ChatEvent, error) {
	if len(channelIDs) == 0 {
		closed := make(chan ChatEvent)
		close(closed)
		return closed, nil
	}
	streams := make([]<-chan ChatEvent, 0, len(channelIDs))
	for _, id := range channelIDs {
		ch, err := s.SubscribeChannel(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("chat: subscribe %d: %w", id, err)
		}
		streams = append(streams, ch)
	}
	return fanIn(ctx, streams), nil
}

// fanIn merges N event streams into one buffered output channel. The output
// closes after every input is drained (which happens when ctx is canceled).
func fanIn(ctx context.Context, streams []<-chan ChatEvent) <-chan ChatEvent {
	out := make(chan ChatEvent, 32)
	var wg sync.WaitGroup
	for _, s := range streams {
		wg.Add(1)
		go func(s <-chan ChatEvent) {
			defer wg.Done()
			for {
				select {
				case ev, ok := <-s:
					if !ok {
						return
					}
					select {
					case out <- ev:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}(s)
	}
	go func() { wg.Wait(); close(out) }()
	return out
}

// Author carries just the denormalized identity fields the bus payload needs.
// Pull from session.Session.Identity at the call site.
type Author struct {
	UserID  int64
	Handle  string
	IsSysop bool
}

// EditLastOwnInChannel rewrites the user's most recent message in the given
// channel with `newBody` and publishes a ChatEventMessageEdited event.
// Returns the updated Message (zero value if the user has no messages in the
// channel to edit).
func (s *ChatService) EditLastOwnInChannel(ctx context.Context, channelID int64, author Author, newBody string) (Message, error) {
	last, err := s.Queries.GetLastOwnMessageInChannel(ctx, gen.GetLastOwnMessageInChannelParams{
		ChannelID: channelID,
		UserID:    author.UserID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Message{}, ErrNoMessageToEdit
		}
		return Message{}, fmt.Errorf("chat: lookup last message: %w", err)
	}
	now := time.Now().UTC()
	updated, err := s.Queries.UpdateChatMessage(ctx, gen.UpdateChatMessageParams{
		ID:       last.ID,
		UserID:   author.UserID,
		Body:     newBody,
		EditedAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return Message{}, fmt.Errorf("chat: update message: %w", err)
	}
	msg := Message{
		ID:        updated.ID,
		ChannelID: updated.ChannelID,
		UserID:    updated.UserID,
		Handle:    author.Handle,
		IsSysop:   author.IsSysop,
		Body:      updated.Body,
		CreatedAt: updated.CreatedAt.Time,
		EditedAt:  updated.EditedAt.Time,
	}
	payload, err := json.Marshal(ChatEvent{
		Kind:      ChatEventMessageEdited,
		MessageID: msg.ID,
		ChannelID: msg.ChannelID,
		UserID:    msg.UserID,
		Handle:    msg.Handle,
		IsSysop:   msg.IsSysop,
		Body:      msg.Body,
		CreatedAt: msg.CreatedAt,
		EditedAt:  msg.EditedAt,
	})
	if err != nil {
		return msg, fmt.Errorf("chat: marshal edit event: %w", err)
	}
	if err := s.Bus.Publish(ctx, T.ChatChannel(channelID), payload); err != nil {
		return msg, fmt.Errorf("chat: publish edit event: %w", err)
	}
	return msg, nil
}

// ErrNoMessageToEdit signals /edit with no prior message in the active
// channel. Callers can surface this as a friendly notice.
var ErrNoMessageToEdit = errors.New("no message to edit")

// React adds a reaction to a message and publishes ReactionAdded. The
// (message_id, user_id, emoji) tuple is unique so this is idempotent —
// hammering the same emoji doesn't inflate counts.
func (s *ChatService) React(ctx context.Context, channelID, messageID int64, author Author, emoji string) error {
	if emoji == "" {
		return fmt.Errorf("chat: empty emoji")
	}
	if err := s.Queries.AddReaction(ctx, gen.AddReactionParams{
		MessageID: messageID,
		UserID:    author.UserID,
		Emoji:     emoji,
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}); err != nil {
		return fmt.Errorf("chat: add reaction: %w", err)
	}
	payload, err := json.Marshal(ChatEvent{
		Kind:      ChatEventReactionAdded,
		MessageID: messageID,
		ChannelID: channelID,
		UserID:    author.UserID,
		Handle:    author.Handle,
		IsSysop:   author.IsSysop,
		Emoji:     emoji,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("chat: marshal reaction event: %w", err)
	}
	return s.Bus.Publish(ctx, T.ChatChannel(channelID), payload)
}

// Unreact removes the user's own reaction (no-op if absent).
func (s *ChatService) Unreact(ctx context.Context, channelID, messageID int64, author Author, emoji string) error {
	if err := s.Queries.RemoveReaction(ctx, gen.RemoveReactionParams{
		MessageID: messageID,
		UserID:    author.UserID,
		Emoji:     emoji,
	}); err != nil {
		return fmt.Errorf("chat: remove reaction: %w", err)
	}
	payload, err := json.Marshal(ChatEvent{
		Kind:      ChatEventReactionRemoved,
		MessageID: messageID,
		ChannelID: channelID,
		UserID:    author.UserID,
		Handle:    author.Handle,
		Emoji:     emoji,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("chat: marshal unreact event: %w", err)
	}
	return s.Bus.Publish(ctx, T.ChatChannel(channelID), payload)
}

// PublishTyping fires a "X is typing" ping on the channel's pub/sub topic.
// No DB row — purely transient. Callers throttle the publish cadence so the
// bus doesn't see one event per keystroke; the receiving screen keys off a
// per-handle expires-at clock and prunes stale entries on its tea.Tick.
func (s *ChatService) PublishTyping(ctx context.Context, channelID int64, author Author) error {
	payload, err := json.Marshal(ChatEvent{
		Kind:      ChatEventTyping,
		ChannelID: channelID,
		UserID:    author.UserID,
		Handle:    author.Handle,
		IsSysop:   author.IsSysop,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("chat: marshal typing event: %w", err)
	}
	return s.Bus.Publish(ctx, T.ChatChannel(channelID), payload)
}

// ReactionsForChannel returns the current reaction count snapshot for every
// message in the channel that has at least one reaction. Used by the screen
// on bootstrap / channel switch to seed the chip display before any live
// events arrive.
func (s *ChatService) ReactionsForChannel(ctx context.Context, channelID int64) (map[int64]map[string]int, error) {
	rows, err := s.Queries.ReactionsForChannel(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("chat: reactions: %w", err)
	}
	out := make(map[int64]map[string]int, len(rows))
	for _, r := range rows {
		if out[r.MessageID] == nil {
			out[r.MessageID] = make(map[string]int)
		}
		out[r.MessageID][r.Emoji] = int(r.N)
	}
	return out, nil
}

// EnsureMembership idempotently records that the user belongs to a channel.
// Called from /join and on first send into an auto-joined channel.
func (s *ChatService) EnsureMembership(ctx context.Context, channelID, userID int64) error {
	return s.Queries.JoinChannelMembership(ctx, gen.JoinChannelMembershipParams{
		ChannelID: channelID,
		UserID:    userID,
		JoinedAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
}

// LeaveMembership removes the user from the channel; downstream the screen
// should switch to #lobby (which the user can never leave — the service
// refuses a leave when name=='lobby').
func (s *ChatService) LeaveMembership(ctx context.Context, channelID, userID int64) error {
	return s.Queries.LeaveChannelMembership(ctx, gen.LeaveChannelMembershipParams{
		ChannelID: channelID,
		UserID:    userID,
	})
}

// JoinedChannels returns every channel the user is a member of, in the order
// the sidebar should render them: #lobby pinned to the top, others alphabetical.
func (s *ChatService) JoinedChannels(ctx context.Context, userID int64) ([]gen.Channel, error) {
	rows, err := s.Queries.ListChannelsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("chat: list joined channels: %w", err)
	}
	out := make([]gen.Channel, 0, len(rows))
	for _, r := range rows {
		out = append(out, gen.Channel(r))
	}
	return out, nil
}

// TouchChannelRead persists "user X has read channel Y up through message Z".
// Idempotent and monotonic (the SQL uses GREATEST so a stale touch can't
// rewind the marker). Used on every channel switch.
func (s *ChatService) TouchChannelRead(ctx context.Context, userID, channelID, latestMsgID int64) error {
	if latestMsgID <= 0 {
		// No messages in the channel yet; nothing to mark. We still want a
		// row to exist so UnreadCounts treats this channel as zero-unread
		// going forward, so write with last_read=0.
	}
	return s.Queries.UpsertChannelRead(ctx, gen.UpsertChannelReadParams{
		UserID:            userID,
		ChannelID:         channelID,
		LastReadMessageID: latestMsgID,
		UpdatedAt:         pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
}

// LatestMessageID returns the highest message ID in a channel, or 0 if empty.
// Used by the screen right before TouchChannelRead so we know what marker
// to write.
func (s *ChatService) LatestMessageID(ctx context.Context, channelID int64) (int64, error) {
	id, err := s.Queries.LatestMessageIDInChannel(ctx, channelID)
	if err != nil {
		return 0, fmt.Errorf("chat: latest message id: %w", err)
	}
	return id, nil
}

// UnreadCounts returns the per-channel unread message count for the user,
// keyed by channel_id. Channels with zero unread are still present in the
// returned map (so the screen can treat absence as "no joined membership").
func (s *ChatService) UnreadCounts(ctx context.Context, userID int64) (map[int64]int, error) {
	rows, err := s.Queries.UnreadCountsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("chat: unread counts: %w", err)
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.ChannelID] = int(r.Unread)
	}
	return out, nil
}

// ResolveDM looks up the target by handle and returns (or creates) the
// deterministic DM channel between the two users. The channel name is
// "dm-<lo>-<hi>" with handles alphabetically sorted, so /dm @bob from alice
// and /dm @alice from bob both land on the same row. Both users get
// channel_members records.
func (s *ChatService) ResolveDM(ctx context.Context, self Author, otherHandle string) (gen.Channel, error) {
	if strings.EqualFold(self.Handle, otherHandle) {
		return gen.Channel{}, ErrCannotDMSelf
	}
	other, err := s.Queries.GetUserByHandle(ctx, otherHandle)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.Channel{}, ErrUnknownHandle
		}
		return gen.Channel{}, fmt.Errorf("chat: lookup dm target: %w", err)
	}
	name := DMChannelName(self.Handle, other.Handle)
	ch, err := s.Queries.GetChannelByName(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		// Lazy-create; both users get joined below.
		ch, err = s.Queries.CreateChannel(ctx, gen.CreateChannelParams{
			Name:        name,
			IsPrivate:   true,
			CreatedByID: &self.UserID,
			CreatedAt:   pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		})
	}
	if err != nil {
		return gen.Channel{}, fmt.Errorf("chat: resolve dm channel: %w", err)
	}
	// Both members idempotently. The ON CONFLICT in the SQL keeps this safe.
	if err := s.EnsureMembership(ctx, ch.ID, self.UserID); err != nil {
		return ch, fmt.Errorf("chat: ensure self membership: %w", err)
	}
	if err := s.EnsureMembership(ctx, ch.ID, other.ID); err != nil {
		return ch, fmt.Errorf("chat: ensure other membership: %w", err)
	}
	return ch, nil
}

// DMChannelName returns the deterministic name for a DM between two handles.
// Lower-cased + alphabetically sorted so the order of args doesn't matter.
func DMChannelName(a, b string) string {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a > b {
		a, b = b, a
	}
	return "dm-" + a + "-" + b
}

// ErrUnknownHandle signals /dm with a handle that isn't registered.
var ErrUnknownHandle = errors.New("unknown handle")

// ErrCannotDMSelf signals /dm @self — we don't open a one-person DM channel.
var ErrCannotDMSelf = errors.New("cannot DM yourself")

// ErrNotFound is returned by mutating ops when the target message/channel is
// missing. Callers usually surface this as a friendly notice.
var ErrNotFound = errors.New("not found")

// ErrForbidden indicates the actor can't perform the operation (e.g. delete
// someone else's message). Callers surface the message as a notice.
type ErrForbidden struct{ Reason string }

func (e ErrForbidden) Error() string { return e.Reason }

// DeleteMessage tombstones a message via deleted_at. Author-only unless
// actor.IsSysop is true. Idempotent on the wire: deleting an already-deleted
// message returns nil (the row stays put). Publishes ChatEventMessageDeleted
// so other sessions can re-render the entry as "(deleted)".
func (s *ChatService) DeleteMessage(ctx context.Context, messageID int64, actor Author) error {
	msg, err := s.Queries.GetChatMessageByID(ctx, messageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("chat: lookup message: %w", err)
	}
	if msg.DeletedAt.Valid {
		return nil
	}
	if msg.UserID != actor.UserID && !actor.IsSysop {
		return ErrForbidden{Reason: "you can only delete your own messages"}
	}
	now := time.Now().UTC()
	if actor.IsSysop && msg.UserID != actor.UserID {
		err = s.Queries.SoftDeleteChatMessageAsSysop(ctx, gen.SoftDeleteChatMessageAsSysopParams{
			ID:        messageID,
			DeletedAt: pgtype.Timestamptz{Time: now, Valid: true},
		})
	} else {
		err = s.Queries.SoftDeleteChatMessage(ctx, gen.SoftDeleteChatMessageParams{
			ID:        messageID,
			UserID:    actor.UserID,
			DeletedAt: pgtype.Timestamptz{Time: now, Valid: true},
		})
	}
	if err != nil {
		return fmt.Errorf("chat: soft-delete: %w", err)
	}
	payload, err := json.Marshal(ChatEvent{
		Kind:      ChatEventMessageDeleted,
		MessageID: messageID,
		ChannelID: msg.ChannelID,
		UserID:    actor.UserID,
		Handle:    actor.Handle,
	})
	if err != nil {
		return fmt.Errorf("chat: marshal delete event: %w", err)
	}
	return s.Bus.Publish(ctx, T.ChatChannel(msg.ChannelID), payload)
}

// SetPinned flips the is_pinned flag and publishes ChatEventPinChanged. Any
// participant can pin/unpin in public channels — role-aware authz is a
// follow-up. Idempotent: setting the same state is a no-op return-nil.
func (s *ChatService) SetPinned(ctx context.Context, messageID int64, pin bool, actor Author) error {
	msg, err := s.Queries.GetChatMessageByID(ctx, messageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("chat: lookup message: %w", err)
	}
	if msg.DeletedAt.Valid {
		return ErrForbidden{Reason: "can't pin a deleted message"}
	}
	if msg.IsPinned == pin {
		return nil
	}
	if err := s.Queries.SetChatMessagePinned(ctx, gen.SetChatMessagePinnedParams{
		ID:       messageID,
		IsPinned: pin,
	}); err != nil {
		return fmt.Errorf("chat: set pinned: %w", err)
	}
	payload, err := json.Marshal(ChatEvent{
		Kind:      ChatEventPinChanged,
		MessageID: messageID,
		ChannelID: msg.ChannelID,
		UserID:    actor.UserID,
		Handle:    actor.Handle,
		IsPinned:  pin,
	})
	if err != nil {
		return fmt.Errorf("chat: marshal pin event: %w", err)
	}
	return s.Bus.Publish(ctx, T.ChatChannel(msg.ChannelID), payload)
}

// ListPins returns the currently-pinned messages for the channel, newest
// pinned first. Capped at 50 server-side.
func (s *ChatService) ListPins(ctx context.Context, channelID int64) ([]Message, error) {
	rows, err := s.Queries.ListPinnedMessagesForChannel(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("chat: list pins: %w", err)
	}
	out := make([]Message, 0, len(rows))
	for _, r := range rows {
		out = append(out, Message{
			ID:              r.ID,
			ChannelID:       r.ChannelID,
			UserID:          r.UserID,
			Handle:          r.AuthorHandle,
			IsSysop:         r.AuthorIsSysop,
			Body:            r.Body,
			CreatedAt:       r.CreatedAt.Time,
			EditedAt:        r.EditedAt.Time,
			DeletedAt:       r.DeletedAt.Time,
			IsPinned:        r.IsPinned,
			ParentMessageID: r.ParentMessageID,
		})
	}
	return out, nil
}

// SetTopic rewrites the channel topic. NULL/empty clears it. Only the channel
// creator (or a sysop) is allowed; a public channel with a NULL created_by_id
// (legacy seeded row) falls back to any-member. Publishes ChatEventTopicChanged
// so every subscriber refreshes their status row.
func (s *ChatService) SetTopic(ctx context.Context, channelID int64, topic *string, actor Author) error {
	if topic != nil && len(*topic) > 200 {
		return ErrForbidden{Reason: "topic must be 200 chars or fewer"}
	}
	if topic != nil {
		t := strings.TrimSpace(*topic)
		if t == "" {
			topic = nil
		} else {
			topic = &t
		}
	}
	channel, err := s.Queries.GetChannelByID(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("chat: lookup channel: %w", err)
	}
	if !canSetTopic(channel, actor) {
		return ErrForbidden{Reason: "only the channel creator can set the topic"}
	}
	currentTopic := ""
	if channel.Topic != nil {
		currentTopic = *channel.Topic
	}
	newTopic := ""
	if topic != nil {
		newTopic = *topic
	}
	if currentTopic == newTopic {
		return nil
	}
	if err := s.Queries.SetChannelTopic(ctx, gen.SetChannelTopicParams{
		ID:    channelID,
		Topic: topic,
	}); err != nil {
		return fmt.Errorf("chat: set topic: %w", err)
	}
	payload, err := json.Marshal(ChatEvent{
		Kind:      ChatEventTopicChanged,
		ChannelID: channelID,
		UserID:    actor.UserID,
		Handle:    actor.Handle,
		Topic:     topic,
	})
	if err != nil {
		return fmt.Errorf("chat: marshal topic event: %w", err)
	}
	return s.Bus.Publish(ctx, T.ChatChannel(channelID), payload)
}

// canSetTopic mirrors ChatAuthorization.CanSetChannelTopic: creator wins, sysop
// wins, and a NULL CreatedByID falls back to any-member (legacy seeded rows).
func canSetTopic(channel gen.Channel, actor Author) bool {
	if actor.IsSysop {
		return true
	}
	if channel.CreatedByID == nil {
		return true
	}
	return *channel.CreatedByID == actor.UserID
}

// Search runs the FTS query against the channel's history and falls back to
// ILIKE if the tsquery parser drops the term (very short or all-punctuation).
// Excludes deleted messages and caps at `limit` rows (clamped to [1, 100]).
func (s *ChatService) Search(ctx context.Context, channelID int64, term string, limit int) ([]Message, error) {
	trimmed := strings.TrimSpace(term)
	if trimmed == "" {
		return nil, nil
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	fts, err := s.Queries.SearchChatMessagesFTS(ctx, gen.SearchChatMessagesFTSParams{
		ChannelID:          channelID,
		WebsearchToTsquery: trimmed,
		Limit:              int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("chat: search fts: %w", err)
	}
	if len(fts) > 0 {
		out := make([]Message, 0, len(fts))
		for _, r := range fts {
			out = append(out, Message{
				ID:              r.ID,
				ChannelID:       r.ChannelID,
				UserID:          r.UserID,
				Handle:          r.AuthorHandle,
				IsSysop:         r.AuthorIsSysop,
				Body:            r.Body,
				CreatedAt:       r.CreatedAt.Time,
				EditedAt:        r.EditedAt.Time,
				IsPinned:        r.IsPinned,
				ParentMessageID: r.ParentMessageID,
			})
		}
		return out, nil
	}
	// Fallback: tsquery dropped the term — try ILIKE. `|` as the escape char
	// so a literal % / _ in the term doesn't expand into a wildcard.
	pattern := ilikeEscape(trimmed)
	rows, err := s.Queries.SearchChatMessagesILike(ctx, gen.SearchChatMessagesILikeParams{
		ChannelID: channelID,
		Body:      "%" + pattern + "%",
		Limit:     int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("chat: search ilike: %w", err)
	}
	out := make([]Message, 0, len(rows))
	for _, r := range rows {
		out = append(out, Message{
			ID:              r.ID,
			ChannelID:       r.ChannelID,
			UserID:          r.UserID,
			Handle:          r.AuthorHandle,
			IsSysop:         r.AuthorIsSysop,
			Body:            r.Body,
			CreatedAt:       r.CreatedAt.Time,
			EditedAt:        r.EditedAt.Time,
			IsPinned:        r.IsPinned,
			ParentMessageID: r.ParentMessageID,
		})
	}
	return out, nil
}

// ilikeEscape rewrites `|` `%` `_` so an ILIKE pattern matches them literally.
func ilikeEscape(s string) string {
	s = strings.ReplaceAll(s, "|", "||")
	s = strings.ReplaceAll(s, "%", "|%")
	s = strings.ReplaceAll(s, "_", "|_")
	return s
}

// ReplyCounts groups child counts by parent_message_id for the visible window
// of messages. Used by the chat screen to hydrate "[N replies]" badges on
// channel history load.
func (s *ChatService) ReplyCounts(ctx context.Context, parentIDs []int64) (map[int64]int, error) {
	if len(parentIDs) == 0 {
		return map[int64]int{}, nil
	}
	rows, err := s.Queries.ReplyCountsForParents(ctx, parentIDs)
	if err != nil {
		return nil, fmt.Errorf("chat: reply counts: %w", err)
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		if r.ParentMessageID != nil {
			out[*r.ParentMessageID] = int(r.N)
		}
	}
	return out, nil
}
