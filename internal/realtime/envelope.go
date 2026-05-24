package realtime

import "time"

// ChatEventKind discriminates the wire-format ChatEvent envelope. New kinds
// added here also need a publisher in ChatService (or wherever).
type ChatEventKind string

const (
	ChatEventMessageCreated  ChatEventKind = "message_created"
	ChatEventMessageEdited   ChatEventKind = "message_edited"
	ChatEventMessageDeleted  ChatEventKind = "message_deleted"
	ChatEventReactionAdded   ChatEventKind = "reaction_added"
	ChatEventReactionRemoved ChatEventKind = "reaction_removed"
	// ChatEventPinChanged covers both pin and unpin — the IsPinned bool on
	// the envelope says which way. Collapsed into one kind because the
	// receiver does the same work either way (rewrap the affected message).
	ChatEventPinChanged ChatEventKind = "pin_changed"
	// ChatEventTopicChanged fans a new topic to every subscriber. Topic is
	// the new value (nil = cleared).
	ChatEventTopicChanged ChatEventKind = "topic_changed"
	// ChatEventTyping is the lightweight "X is typing" ping. No DB row — pub/
	// sub only. Receivers refresh their per-handle expires-at clock; entries
	// older than typingTTL get pruned by the ChatScreen's housekeeping tick.
	ChatEventTyping ChatEventKind = "typing"
)

// ChatEvent is the wire-format envelope published to chat:channel:<id> topics.
// JSON-encoded; the omitempty tags keep the payload tight for the most common
// "new message" case. For edit events Body carries the NEW body and EditedAt
// carries the mutation timestamp; CreatedAt is preserved across edits.
type ChatEvent struct {
	Kind      ChatEventKind `json:"kind"`
	MessageID int64         `json:"message_id"`
	ChannelID int64         `json:"channel_id"`
	UserID    int64         `json:"user_id,omitempty"`
	Handle    string        `json:"handle,omitempty"`
	IsSysop   bool          `json:"is_sysop,omitempty"`
	Body            string    `json:"body,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
	EditedAt        time.Time `json:"edited_at,omitempty"`
	ParentMessageID *int64    `json:"parent_message_id,omitempty"`
	Emoji           string    `json:"emoji,omitempty"` // populated on reaction events
	// IsPinned is the new state for ChatEventPinChanged. Marshaled even when
	// false so the receiver can distinguish pin → unpin.
	IsPinned bool `json:"is_pinned,omitempty"`
	// Topic is the new channel topic for ChatEventTopicChanged (nil = cleared).
	Topic *string `json:"topic,omitempty"`
}
