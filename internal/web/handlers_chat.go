package web

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/csrf"
	"github.com/jackc/pgx/v5"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/realtime"
	tuichat "github.com/nickna/ssh.night.ms/internal/tui/chat"
)

// Chat is the server-rendered web view of the realtime chat feature. It reuses
// the same realtime.ChatService the SSH/TUI path uses (via h.deps.Chat) so the
// two surfaces share one source of truth — a message sent here arrives live in
// an SSH session and vice-versa.
//
// Unlike Boards (public reads), every /chat route requires a session: chat is
// interactive, needs identity for read-markers, and DMs are private. Anonymous
// requests redirect to /login.
//
// New messages reach an open channel page live over Server-Sent Events
// (chatStream); sending stays an ordinary form POST that redirects back. The
// fresh page load re-renders history (including the just-sent message) and
// opens a new SSE subscription that only streams *future* events, so the
// sender never sees their own message twice.

const (
	// chatHistoryLimit is how many recent messages the channel page seeds with.
	// Matches the TUI's initial RecentMessages(100) load.
	chatHistoryLimit = 100

	// chatBodyMax mirrors the TUI compose cap so the two surfaces reject the
	// same oversized bodies.
	chatBodyMax = 4000

	// chatChannelNameMax bounds a join-by-name request. Channel names are short
	// BBS-style slugs; this is generous.
	chatChannelNameMax = 64

	// chatStreamHeartbeat is how often the SSE loop emits a comment line to keep
	// intermediaries from idling the connection closed.
	chatStreamHeartbeat = 25 * time.Second
)

// chatReactionPalette is the fixed quick-pick set the web UI offers. Reactions
// from the SSH /react command can use any emoji-table glyph and still render as
// chips here, but the web react endpoint only accepts these so it can't be used
// to store arbitrary strings.
var chatReactionPalette = []string{"👍", "❤️", "😂", "🎉", "🔥", "👀"}

// validReaction reports whether emoji is one the web UI is allowed to add.
func validReaction(emoji string) bool {
	for _, e := range chatReactionPalette {
		if e == emoji {
			return true
		}
	}
	return false
}

// renderChatBody converts a raw message body to safe HTML matching the TUI
// markup: *bold*, _italic_, `code`, @mentions (self-highlighted per viewer),
// and :emoji: shortcodes (TokenizeBody substitutes those internally). A leading
// "/me " marks an action message — the prefix is stripped and the bool return
// is true so the caller can render it as "* @handle <action>". Every token's
// text is HTML-escaped, so the result is safe to emit raw / set as innerHTML.
func renderChatBody(body, selfHandle string) (template.HTML, bool) {
	isAction := false
	if strings.HasPrefix(body, tuichat.MeMarker) {
		isAction = true
		body = strings.TrimPrefix(body, tuichat.MeMarker)
	}
	tokens, _ := tuichat.TokenizeBody(body, selfHandle)
	var b strings.Builder
	for _, t := range tokens {
		esc := template.HTMLEscapeString(t.Text)
		switch t.Kind {
		case tuichat.BodyBold:
			b.WriteString("<strong>" + esc + "</strong>")
		case tuichat.BodyItalic:
			b.WriteString("<em>" + esc + "</em>")
		case tuichat.BodyCode:
			b.WriteString("<code>" + esc + "</code>")
		case tuichat.BodyMentionOther:
			b.WriteString(`<span class="mention">` + esc + `</span>`)
		case tuichat.BodyMentionSelf:
			b.WriteString(`<span class="mention-self">` + esc + `</span>`)
		default:
			b.WriteString(esc)
		}
	}
	return template.HTML(b.String()), isAction
}

// dmPartner derives the other participant's handle from a "dm-<lo>-<hi>"
// channel name. Because the two handles are lowercased and alphabetically
// sorted by DMChannelName, and self can never equal partner (no self-DMs),
// stripping the known self handle from the front or back is unambiguous even
// when handles themselves contain hyphens.
func dmPartner(channelName, selfHandle string) string {
	rest := strings.TrimPrefix(channelName, "dm-")
	self := strings.ToLower(selfHandle)
	switch {
	case strings.HasPrefix(rest, self+"-"):
		return rest[len(self)+1:]
	case strings.HasSuffix(rest, "-"+self):
		return rest[:len(rest)-len(self)-1]
	default:
		return rest
	}
}

// isMember reports whether the user belongs to the channel. Reused for the
// private-channel (DM) access gate on view/send/stream. Implemented via the
// existing JoinedChannels list to avoid a new sqlc query.
func (h *handlers) isMember(r *http.Request, userID, channelID int64) bool {
	chans, err := h.deps.Chat.JoinedChannels(r.Context(), userID)
	if err != nil {
		h.deps.Logger.Warn("chat: membership check", "user_id", userID, "channel", channelID, "err", err)
		return false
	}
	for _, c := range chans {
		if c.ID == channelID {
			return true
		}
	}
	return false
}

//
// Channel list — GET /chat
//

type chatChannelItem struct {
	ID     int64
	Name   string // display form: "#lobby" for public, "@partner" for a DM
	IsDM   bool
	Unread int
}

type chatIndexData struct {
	pageData
	Channels []chatChannelItem // public channels
	DMs      []chatChannelItem // direct messages
	Notice   string
}

func (h *handlers) chatIndex(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	// Ensure the user is in #lobby so the list is never empty on first visit —
	// mirrors the TUI's bootstrap auto-join. Best-effort: a failure here just
	// means #lobby may be absent from the list, it doesn't block the page.
	if lobby, err := h.deps.Chat.ResolvePublicChannel(r.Context(), "lobby", id.UserID); err == nil {
		if err := h.deps.Chat.EnsureMembership(r.Context(), lobby.ID, id.UserID); err != nil {
			h.deps.Logger.Warn("chat: ensure lobby membership", "user_id", id.UserID, "err", err)
		}
	} else {
		h.deps.Logger.Warn("chat: resolve lobby", "user_id", id.UserID, "err", err)
	}
	chans, err := h.deps.Chat.JoinedChannels(r.Context(), id.UserID)
	if err != nil {
		h.deps.Logger.Error("chat: joined channels", "user_id", id.UserID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var unread map[int64]int
	if u, err := h.deps.Chat.UnreadCounts(r.Context(), id.UserID); err == nil {
		unread = u
	} else {
		h.deps.Logger.Warn("chat: unread counts", "user_id", id.UserID, "err", err)
	}

	var public, dms []chatChannelItem
	for _, c := range chans {
		if c.IsPrivate {
			dms = append(dms, chatChannelItem{
				ID:     c.ID,
				Name:   "@" + dmPartner(c.Name, id.Handle),
				IsDM:   true,
				Unread: unread[c.ID],
			})
			continue
		}
		public = append(public, chatChannelItem{
			ID:     c.ID,
			Name:   "#" + c.Name,
			Unread: unread[c.ID],
		})
	}

	h.renderProfile(w, http.StatusOK, "chat_index", chatIndexData{
		pageData: h.basePage(r, "chat"),
		Channels: public,
		DMs:      dms,
		Notice:   chatIndexNotice(r.URL.Query().Get("err")),
	})
}

func chatIndexNotice(code string) string {
	switch code {
	case "nouser":
		return "no user with that handle"
	case "self":
		return "you can't DM yourself"
	case "badname":
		return "invalid channel name"
	}
	return ""
}

//
// Channel view — GET /chat/{channelID}
//

type chatHeader struct {
	ID    int64
	Name  string // display form ("#lobby" / "@partner")
	Topic string
	IsDM  bool
}

// reactionChip is one emoji + its count on a message, with Mine set when the
// viewer is among the reactors (drives the toggle highlight + add/remove pick).
type reactionChip struct {
	Emoji string
	Count int
	Mine  bool
}

type chatMessageItem struct {
	ID           int64
	Handle       string
	IsSysop      bool
	Body         template.HTML
	RawBody      string // raw text for the edit box; only set on own messages
	Time         string // pre-localized stamp (viewer's zone + clock format)
	IsOwn        bool
	Edited       bool
	IsPinned     bool
	IsAction     bool   // "/me" action line
	ParentID     int64  // 0 = top-level; else the message this replies to
	ParentHandle string // resolved within the loaded window; "" if out of window
	Reactions    []reactionChip
	ReplyCount   int
}

type pinnedItem struct {
	ID     int64
	Handle string
	Body   template.HTML
}

type chatChannelData struct {
	pageData
	Channel    chatHeader
	Messages   []chatMessageItem
	Pinned     []pinnedItem
	Notice     string
	SelfHandle string
	IsSysop    bool
	CSRFToken  string
	Palette    []string
}

func (h *handlers) chatChannel(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	ch, ok := h.loadChannelForUser(w, r, id)
	if !ok {
		return
	}

	msgs, err := h.deps.Chat.RecentMessages(r.Context(), ch.ID, chatHistoryLimit)
	if err != nil {
		h.deps.Logger.Error("chat: recent messages", "channel", ch.ID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Map message ID → author handle across the window so a reply can name the
	// message it threads under (parents outside the window stay unnamed).
	parentHandles := make(map[int64]string, len(msgs))
	visibleIDs := make([]int64, 0, len(msgs))
	for _, m := range msgs {
		if m.DeletedAt.IsZero() {
			parentHandles[m.ID] = m.Handle
			visibleIDs = append(visibleIDs, m.ID)
		}
	}

	// Bootstrap the live-feature overlays. All best-effort: a failure drops the
	// overlay rather than blocking the page.
	reactions, err := h.deps.Chat.ReactionsForChannel(r.Context(), ch.ID)
	if err != nil {
		h.deps.Logger.Warn("chat: reactions", "channel", ch.ID, "err", err)
	}
	mine, err := h.deps.Chat.MyReactionsForChannel(r.Context(), ch.ID, id.UserID)
	if err != nil {
		h.deps.Logger.Warn("chat: my reactions", "channel", ch.ID, "err", err)
	}
	replyCounts, err := h.deps.Chat.ReplyCounts(r.Context(), visibleIDs)
	if err != nil {
		h.deps.Logger.Warn("chat: reply counts", "channel", ch.ID, "err", err)
	}

	items := make([]chatMessageItem, 0, len(msgs))
	for _, m := range msgs {
		if !m.DeletedAt.IsZero() {
			continue // soft-deleted; hidden from history (live deletes show a tombstone)
		}
		body, isAction := renderChatBody(m.Body, id.Handle)
		item := chatMessageItem{
			ID:         m.ID,
			Handle:     m.Handle,
			IsSysop:    m.IsSysop,
			Body:       body,
			Time:       id.Prefs.FormatStamp(m.CreatedAt),
			IsOwn:      m.UserID == id.UserID,
			Edited:     !m.EditedAt.IsZero(),
			IsPinned:   m.IsPinned,
			IsAction:   isAction,
			Reactions:  buildReactionChips(reactions[m.ID], mine[m.ID]),
			ReplyCount: replyCounts[m.ID],
		}
		if item.IsOwn {
			item.RawBody = m.Body
		}
		if m.ParentMessageID != nil {
			item.ParentID = *m.ParentMessageID
			item.ParentHandle = parentHandles[*m.ParentMessageID] // "" if out of window
		}
		items = append(items, item)
	}

	var pinned []pinnedItem
	if pins, err := h.deps.Chat.ListPins(r.Context(), ch.ID); err == nil {
		for _, p := range pins {
			body, _ := renderChatBody(p.Body, id.Handle)
			pinned = append(pinned, pinnedItem{ID: p.ID, Handle: p.Handle, Body: body})
		}
	} else {
		h.deps.Logger.Warn("chat: list pins", "channel", ch.ID, "err", err)
	}

	// Clear the unread badge for this user — shared with the SSH path, so
	// reading here marks it read over SSH too. Best-effort.
	if latest, err := h.deps.Chat.LatestMessageID(r.Context(), ch.ID); err == nil {
		if err := h.deps.Chat.TouchChannelRead(r.Context(), id.UserID, ch.ID, latest); err != nil {
			h.deps.Logger.Warn("chat: touch read", "channel", ch.ID, "err", err)
		}
	}

	h.renderProfile(w, http.StatusOK, "chat_channel", chatChannelData{
		pageData:   h.basePage(r, chatDisplayName(ch, id.Handle)),
		Channel:    chatChannelHeader(ch, id.Handle),
		Messages:   items,
		Pinned:     pinned,
		Notice:     chatChannelNotice(r.URL.Query().Get("err")),
		SelfHandle: id.Handle,
		IsSysop:    id.IsSysop,
		CSRFToken:  csrf.Token(r),
		Palette:    chatReactionPalette,
	})
}

// buildReactionChips turns the count map (emoji→count) plus the viewer's own
// set into a stable, alphabetically-ordered chip slice.
func buildReactionChips(counts map[string]int, mine map[string]bool) []reactionChip {
	if len(counts) == 0 {
		return nil
	}
	emojis := make([]string, 0, len(counts))
	for e := range counts {
		emojis = append(emojis, e)
	}
	sort.Strings(emojis)
	chips := make([]reactionChip, 0, len(emojis))
	for _, e := range emojis {
		chips = append(chips, reactionChip{Emoji: e, Count: counts[e], Mine: mine[e]})
	}
	return chips
}

func chatChannelNotice(code string) string {
	switch code {
	case "empty":
		return "message can't be empty"
	case "toolong":
		return "message too long (4000 characters max)"
	}
	return ""
}

//
// Send — POST /chat/{channelID}/send
//

func (h *handlers) chatSend(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	ch, ok := h.loadChannelForUser(w, r, id)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	body := strings.TrimSpace(r.PostFormValue("body"))
	channelPath := "/chat/" + strconv.FormatInt(ch.ID, 10)

	switch {
	case body == "":
		http.Redirect(w, r, channelPath+"?err=empty", http.StatusSeeOther)
		return
	case len([]rune(body)) > chatBodyMax:
		http.Redirect(w, r, channelPath+"?err=toolong", http.StatusSeeOther)
		return
	}

	// Auto-join public channels on first send so they appear in the user's
	// channel list afterward (mirrors the TUI's auto-join-on-send behavior).
	// DMs already have both members from ResolveDM.
	if !ch.IsPrivate {
		if err := h.deps.Chat.EnsureMembership(r.Context(), ch.ID, id.UserID); err != nil {
			h.deps.Logger.Warn("chat: ensure membership on send", "channel", ch.ID, "err", err)
		}
	}

	// Optional parent_id threads this message under another (the "reply"
	// affordance). A zero/invalid value just sends a top-level message.
	var parentID *int64
	if pid, perr := strconv.ParseInt(r.PostFormValue("parent_id"), 10, 64); perr == nil && pid > 0 {
		parentID = &pid
	}

	author := realtime.Author{UserID: id.UserID, Handle: id.Handle, IsSysop: id.IsSysop}
	if _, err := h.deps.Chat.SendReply(r.Context(), ch.ID, author, body, parentID); err != nil {
		h.deps.Logger.Error("chat: send", "channel", ch.ID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, channelPath, http.StatusSeeOther)
}

//
// Join a public channel by name — POST /chat/join
//

func (h *handlers) chatJoin(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("name")))
	name = strings.TrimPrefix(name, "#")
	if name == "" || len(name) > chatChannelNameMax || strings.HasPrefix(name, "dm-") {
		http.Redirect(w, r, "/chat?err=badname", http.StatusSeeOther)
		return
	}
	ch, err := h.deps.Chat.ResolvePublicChannel(r.Context(), name, id.UserID)
	if err != nil {
		h.deps.Logger.Error("chat: resolve public channel", "name", name, "err", err)
		http.Redirect(w, r, "/chat?err=badname", http.StatusSeeOther)
		return
	}
	if err := h.deps.Chat.EnsureMembership(r.Context(), ch.ID, id.UserID); err != nil {
		h.deps.Logger.Error("chat: join membership", "channel", ch.ID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/chat/"+strconv.FormatInt(ch.ID, 10), http.StatusSeeOther)
}

//
// Start a DM by handle — POST /chat/dm
//

func (h *handlers) chatStartDM(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	handle := strings.TrimPrefix(strings.TrimSpace(r.PostFormValue("handle")), "@")
	if handle == "" {
		http.Redirect(w, r, "/chat?err=nouser", http.StatusSeeOther)
		return
	}
	author := realtime.Author{UserID: id.UserID, Handle: id.Handle, IsSysop: id.IsSysop}
	ch, err := h.deps.Chat.ResolveDM(r.Context(), author, handle)
	switch {
	case errors.Is(err, realtime.ErrUnknownHandle):
		http.Redirect(w, r, "/chat?err=nouser", http.StatusSeeOther)
		return
	case errors.Is(err, realtime.ErrCannotDMSelf):
		http.Redirect(w, r, "/chat?err=self", http.StatusSeeOther)
		return
	case err != nil:
		h.deps.Logger.Error("chat: resolve dm", "handle", handle, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/chat/"+strconv.FormatInt(ch.ID, 10), http.StatusSeeOther)
}

//
// Message actions — react / unreact / pin / edit / delete
//
// These are fired by chat.js with fetch() and return 204 on success (no body,
// no redirect). The resulting Redis event fans out over SSE and patches every
// open client — including the actor's. All are login + membership gated.
//

// chatActionError maps a ChatService error to an HTTP status for the JSON-fetch
// endpoints. It returns true if it handled (wrote) an error response.
func (h *handlers) chatActionError(w http.ResponseWriter, op string, channelID int64, err error) bool {
	if err == nil {
		return false
	}
	var forbidden realtime.ErrForbidden
	switch {
	case errors.As(err, &forbidden):
		http.Error(w, forbidden.Reason, http.StatusForbidden)
	case errors.Is(err, realtime.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, realtime.ErrNoMessageToEdit):
		http.Error(w, "no message to edit", http.StatusBadRequest)
	default:
		h.deps.Logger.Error("chat: "+op, "channel", channelID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
	return true
}

// chatActionSetup performs the shared gate for the fetch endpoints: requires a
// session, resolves + access-checks the channel, and parses the form. On any
// failure it writes the response and returns ok=false.
func (h *handlers) chatActionSetup(w http.ResponseWriter, r *http.Request) (*webIdentity, gen.Channel, bool) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil, gen.Channel{}, false
	}
	ch, ok := h.loadChannelForUser(w, r, id)
	if !ok {
		return nil, gen.Channel{}, false
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return nil, gen.Channel{}, false
	}
	return id, ch, true
}

func (h *handlers) chatReact(w http.ResponseWriter, r *http.Request) {
	id, ch, ok := h.chatActionSetup(w, r)
	if !ok {
		return
	}
	mid, _ := strconv.ParseInt(r.PostFormValue("message_id"), 10, 64)
	emoji := r.PostFormValue("emoji")
	if mid <= 0 || !validReaction(emoji) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	author := realtime.Author{UserID: id.UserID, Handle: id.Handle, IsSysop: id.IsSysop}
	if h.chatActionError(w, "react", ch.ID, h.deps.Chat.React(r.Context(), ch.ID, mid, author, emoji)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) chatUnreact(w http.ResponseWriter, r *http.Request) {
	id, ch, ok := h.chatActionSetup(w, r)
	if !ok {
		return
	}
	mid, _ := strconv.ParseInt(r.PostFormValue("message_id"), 10, 64)
	emoji := r.PostFormValue("emoji")
	if mid <= 0 || emoji == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	author := realtime.Author{UserID: id.UserID, Handle: id.Handle, IsSysop: id.IsSysop}
	if h.chatActionError(w, "unreact", ch.ID, h.deps.Chat.Unreact(r.Context(), ch.ID, mid, author, emoji)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) chatPin(w http.ResponseWriter, r *http.Request) {
	id, ch, ok := h.chatActionSetup(w, r)
	if !ok {
		return
	}
	mid, _ := strconv.ParseInt(r.PostFormValue("message_id"), 10, 64)
	if mid <= 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pin := r.PostFormValue("pin") == "true" || r.PostFormValue("pin") == "1"
	author := realtime.Author{UserID: id.UserID, Handle: id.Handle, IsSysop: id.IsSysop}
	if h.chatActionError(w, "pin", ch.ID, h.deps.Chat.SetPinned(r.Context(), mid, pin, author)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) chatEdit(w http.ResponseWriter, r *http.Request) {
	id, ch, ok := h.chatActionSetup(w, r)
	if !ok {
		return
	}
	body := strings.TrimSpace(r.PostFormValue("body"))
	if body == "" {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}
	if len([]rune(body)) > chatBodyMax {
		http.Error(w, "too long", http.StatusBadRequest)
		return
	}
	author := realtime.Author{UserID: id.UserID, Handle: id.Handle, IsSysop: id.IsSysop}
	_, err := h.deps.Chat.EditLastOwnInChannel(r.Context(), ch.ID, author, body)
	if h.chatActionError(w, "edit", ch.ID, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) chatDelete(w http.ResponseWriter, r *http.Request) {
	id, ch, ok := h.chatActionSetup(w, r)
	if !ok {
		return
	}
	mid, _ := strconv.ParseInt(r.PostFormValue("message_id"), 10, 64)
	if mid <= 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	author := realtime.Author{UserID: id.UserID, Handle: id.Handle, IsSysop: id.IsSysop}
	if h.chatActionError(w, "delete", ch.ID, h.deps.Chat.DeleteMessage(r.Context(), mid, author)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

//
// Live stream — GET /chat/{channelID}/stream (Server-Sent Events)
//

// chatStreamFrame is the tagged JSON payload pushed per live event. Kind
// selects which fields are meaningful; omitempty keeps each frame tight. Body
// is already-escaped HTML from renderChatBody (safe as innerHTML); Handle/Time
// are plain text the client sets via textContent. Boolean fields are absent
// when false — the client treats absence as false.
type chatStreamFrame struct {
	Kind     string `json:"kind"`
	ID       int64  `json:"id,omitempty"`
	Handle   string `json:"handle,omitempty"`
	IsSysop  bool   `json:"is_sysop,omitempty"`
	Body     string `json:"body,omitempty"`
	Raw      string `json:"raw,omitempty"` // raw text for the edit box (own messages)
	Time     string `json:"time,omitempty"`
	ParentID int64  `json:"parent_id,omitempty"`
	IsOwn    bool   `json:"is_own,omitempty"`
	IsAction bool   `json:"is_action,omitempty"`
	Emoji    string `json:"emoji,omitempty"`   // reaction events
	IsSelf   bool   `json:"is_self,omitempty"` // reaction by the viewer
	IsPinned bool   `json:"is_pinned,omitempty"`
}

func (h *handlers) chatStream(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	ch, ok := h.loadChannelForUser(w, r, id)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	events, err := h.deps.Chat.SubscribeChannel(r.Context(), ch.ID)
	if err != nil {
		h.deps.Logger.Error("chat: subscribe", "channel", ch.ID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(": connected\n\n")); err != nil {
		return
	}
	flusher.Flush()

	// writeFrame marshals + writes one SSE data frame. Returns false if the
	// write failed (client gone) so the loop can exit.
	writeFrame := func(f chatStreamFrame) bool {
		payload, err := json.Marshal(f)
		if err != nil {
			h.deps.Logger.Warn("chat: marshal stream frame", "channel", ch.ID, "err", err)
			return true
		}
		if _, err := w.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	heartbeat := time.NewTicker(chatStreamHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			f, send := h.streamFrame(ev, id)
			if !send {
				continue // topic/typing — not surfaced on web
			}
			if !writeFrame(f) {
				return
			}
		}
	}
}

// streamFrame maps a realtime.ChatEvent to the SSE frame for a specific viewer
// (so mention-self highlighting and is_own/is_self are per-connection). The
// bool is false for event kinds the web doesn't render (topic/typing).
func (h *handlers) streamFrame(ev realtime.ChatEvent, viewer *webIdentity) (chatStreamFrame, bool) {
	switch ev.Kind {
	case realtime.ChatEventMessageCreated:
		body, isAction := renderChatBody(ev.Body, viewer.Handle)
		f := chatStreamFrame{
			Kind:     "message_created",
			ID:       ev.MessageID,
			Handle:   ev.Handle,
			IsSysop:  ev.IsSysop,
			Body:     string(body),
			Time:     viewer.Prefs.FormatStamp(ev.CreatedAt),
			IsOwn:    ev.UserID == viewer.UserID,
			IsAction: isAction,
		}
		if ev.ParentMessageID != nil {
			f.ParentID = *ev.ParentMessageID
		}
		if f.IsOwn {
			f.Raw = ev.Body
		}
		return f, true
	case realtime.ChatEventMessageEdited:
		body, _ := renderChatBody(ev.Body, viewer.Handle)
		f := chatStreamFrame{
			Kind:  "message_edited",
			ID:    ev.MessageID,
			Body:  string(body),
			IsOwn: ev.UserID == viewer.UserID,
		}
		if f.IsOwn {
			f.Raw = ev.Body
		}
		return f, true
	case realtime.ChatEventMessageDeleted:
		return chatStreamFrame{Kind: "message_deleted", ID: ev.MessageID}, true
	case realtime.ChatEventReactionAdded:
		return chatStreamFrame{
			Kind:   "reaction_added",
			ID:     ev.MessageID,
			Emoji:  ev.Emoji,
			Handle: ev.Handle,
			IsSelf: ev.UserID == viewer.UserID,
		}, true
	case realtime.ChatEventReactionRemoved:
		return chatStreamFrame{
			Kind:   "reaction_removed",
			ID:     ev.MessageID,
			Emoji:  ev.Emoji,
			Handle: ev.Handle,
			IsSelf: ev.UserID == viewer.UserID,
		}, true
	case realtime.ChatEventPinChanged:
		return chatStreamFrame{Kind: "pin_changed", ID: ev.MessageID, IsPinned: ev.IsPinned}, true
	default:
		return chatStreamFrame{}, false
	}
}

//
// Shared helpers
//

// loadChannelForUser resolves the {channelID} URL param to a channel and
// enforces the access gate: public channels are open to any signed-in user;
// private channels (DMs) require membership. It writes the 404/500 response on
// failure and returns ok=false so the caller can bail.
func (h *handlers) loadChannelForUser(w http.ResponseWriter, r *http.Request, id *webIdentity) (gen.Channel, bool) {
	channelID, ok := parseID(r, "channelID")
	if !ok {
		http.NotFound(w, r)
		return gen.Channel{}, false
	}
	ch, err := h.deps.Queries.GetChannelByID(r.Context(), channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return gen.Channel{}, false
		}
		h.deps.Logger.Error("chat: get channel", "channel", channelID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return gen.Channel{}, false
	}
	if ch.IsPrivate && !h.isMember(r, id.UserID, ch.ID) {
		http.NotFound(w, r) // don't reveal that a private channel exists
		return gen.Channel{}, false
	}
	return ch, true
}

// chatDisplayName returns the human title for a channel ("#lobby" or "@bob").
func chatDisplayName(ch gen.Channel, selfHandle string) string {
	if ch.IsPrivate {
		return "@" + dmPartner(ch.Name, selfHandle)
	}
	return "#" + ch.Name
}

func chatChannelHeader(ch gen.Channel, selfHandle string) chatHeader {
	topic := ""
	if ch.Topic != nil {
		topic = *ch.Topic
	}
	return chatHeader{
		ID:    ch.ID,
		Name:  chatDisplayName(ch, selfHandle),
		Topic: topic,
		IsDM:  ch.IsPrivate,
	}
}
