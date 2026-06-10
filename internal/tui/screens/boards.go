package screens

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/art"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Boards is the discussion-forum screen. It owns its own internal mini-router
// (forum list -> topic list -> thread). Esc walks back through the modes;
// Esc on the forum list returns to the lobby.
type Boards struct {
	sess *session.Session

	mode boardsMode
	err  string

	// navSeq is bumped on every view-changing transition (drill-in and
	// Esc-back). Each topics/posts load captures it and the loaded handler
	// drops the response if it no longer matches — so a slow response can't
	// paint a forum/topic the user already navigated away from, nor yank them
	// back into a thread they Esc'd out of.
	navSeq int

	forums        []realtime.Forum
	forumCursor   int
	unreadByForum map[int64]int // forum_id → unread post count; nil until first load

	activeForum   *realtime.Forum
	topics        []realtime.Topic
	topicCursor   int
	unreadByTopic map[int64]int // topic_id → unread; refreshed on every topic-list load

	activeTopic *realtime.Topic
	posts       []realtime.Post
	postScroll  int

	// Compose state. Title stays a textinput (single line); body uses a
	// bubbles textarea so users can paste multi-line content + insert
	// blank lines. Submission is Ctrl+S — Enter alone inserts a newline.
	composeTitle textinput.Model
	composeBody  textarea.Model
	composeStage composeStage
	// composeFocus picks the field that consumes typing during stageNew. 0 =
	// title (single-line), 1 = body (textarea). Tab toggles. stageReply
	// always lands on the body and ignores this field.
	composeFocus int
	composeErr   string
}

type composeStage int

const (
	stageNone composeStage = iota
	// stageNew is the merged new-topic form: title + body visible together;
	// composeFocus picks which field consumes typing.
	stageNew
	stageReply
)

type boardsMode int

const (
	modeForumList boardsMode = iota
	modeTopicList
	modeThread
	modeCompose
)

const topicListLimit = 100

func NewBoards(sess *session.Session) tea.Model {
	t := textinput.New()
	t.Placeholder = "topic title…"
	t.CharLimit = 120
	b := textarea.New()
	b.Placeholder = "your message — Enter inserts a newline · Ctrl+S to submit · Esc to cancel"
	b.CharLimit = 4000
	b.SetWidth(76)
	b.SetHeight(8)
	b.ShowLineNumbers = false
	return &Boards{sess: sess, mode: modeForumList, composeTitle: t, composeBody: b}
}

//
// Msg envelopes
//

type boardsForumsLoadedMsg struct {
	forums []realtime.Forum
	unread map[int64]int // forum_id → unread (may be nil if the aggregate fails)
}
type boardsTopicsLoadedMsg struct {
	seq    int
	forum  realtime.Forum
	topics []realtime.Topic
	unread map[int64]int // topic_id → unread
}
type boardsPostsLoadedMsg struct {
	seq   int
	topic realtime.Topic
	posts []realtime.Post
}
type boardsErrMsg struct {
	stage string
	err   error
}

// boardsReadTouchedMsg is the no-op result of a fire-and-forget TouchTopicRead.
// Carries any error for logging; never changes UI state.
type boardsReadTouchedMsg struct{ err error }

func (m *Boards) Init() tea.Cmd { return m.loadForums() }

func (m *Boards) loadForums() tea.Cmd {
	svc := m.sess.Forums
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		fs, err := svc.ListForums(ctx)
		if err != nil {
			return boardsErrMsg{stage: "list forums", err: err}
		}
		// Unread aggregate is best-effort: a failure here logs through
		// the service-side logger and the screen falls back to "no badges"
		// rather than aborting the list.
		unread, _ := svc.UnreadCountsByForum(ctx, userID)
		return boardsForumsLoadedMsg{forums: fs, unread: unread}
	}
}

func (m *Boards) loadTopics(forum realtime.Forum) tea.Cmd {
	svc := m.sess.Forums
	userID := m.sess.Identity.UserID
	m.navSeq++
	seq := m.navSeq
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		ts, err := svc.RecentTopics(ctx, forum.ID, topicListLimit)
		if err != nil {
			return boardsErrMsg{stage: "topics", err: err}
		}
		unread, _ := svc.UnreadTopicCounts(ctx, userID, forum.ID)
		return boardsTopicsLoadedMsg{seq: seq, forum: forum, topics: ts, unread: unread}
	}
}

// touchRead persists the user's "I have read this topic up to the latest
// post" marker. Fire-and-forget — the screen never blocks on it and never
// shows the result, but errors do log via the service.
func (m *Boards) touchRead(topicID int64) tea.Cmd {
	svc := m.sess.Forums
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		return boardsReadTouchedMsg{err: svc.TouchTopicRead(ctx, userID, topicID)}
	}
}

func (m *Boards) loadPosts(topic realtime.Topic) tea.Cmd {
	svc := m.sess.Forums
	m.navSeq++
	seq := m.navSeq
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		ps, err := svc.Posts(ctx, topic.ID)
		if err != nil {
			return boardsErrMsg{stage: "posts", err: err}
		}
		return boardsPostsLoadedMsg{seq: seq, topic: topic, posts: ps}
	}
}

func (m *Boards) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case boardsForumsLoadedMsg:
		m.forums = msg.forums
		m.unreadByForum = msg.unread
		m.forumCursor = clampIndex(m.forumCursor, len(m.forums))

	case boardsTopicsLoadedMsg:
		if msg.seq != m.navSeq {
			return m, nil // stale: user navigated away before this landed
		}
		f := msg.forum
		m.activeForum = &f
		m.topics = msg.topics
		m.unreadByTopic = msg.unread
		m.topicCursor = 0
		m.mode = modeTopicList

	case boardsPostsLoadedMsg:
		if msg.seq != m.navSeq {
			return m, nil // stale: user navigated away before this landed
		}
		t := msg.topic
		m.activeTopic = &t
		m.posts = msg.posts
		m.postScroll = 0
		m.mode = modeThread
		// Mark the topic read now that the user has the thread on screen.
		// The unreadByTopic map is stale until the next loadTopics, so we
		// also zero this topic's entry so the ● flips to ○ when the user
		// Esc's back without an extra round-trip.
		if m.unreadByTopic != nil {
			m.unreadByTopic[t.ID] = 0
		}
		return m, m.touchRead(t.ID)

	case boardsTopicCreatedMsg:
		// After a successful new topic, navigate INTO the new thread.
		t := msg.topic
		m.activeTopic = &t
		m.posts = msg.posts
		m.postScroll = 0
		m.composeStage = stageNone
		m.composeErr = ""
		m.composeTitle.SetValue("")
		m.composeBody.SetValue("")
		m.mode = modeThread
		return m, m.touchRead(t.ID)

	case boardsPostCreatedMsg:
		// After a reply, reload the thread so the new post shows up.
		m.composeStage = stageNone
		m.composeErr = ""
		m.composeBody.SetValue("")
		m.mode = modeThread
		if m.activeTopic != nil {
			return m, m.loadPosts(*m.activeTopic)
		}

	case boardsReadTouchedMsg:
		// Pure side-effect cmd. Errors are already logged via the
		// service-side Logger; nothing for the screen to do.
		if msg.err != nil {
			m.sess.Logger.Warn("boards: touch read", "err", msg.err)
		}

	case boardsErrMsg:
		m.err = fmt.Sprintf("%s: %v", msg.stage, msg.err)
		m.sess.Logger.Error("boards", "stage", msg.stage, "err", msg.err)

	case tea.KeyMsg:
		switch m.mode {
		case modeForumList:
			return m.handleForumListKey(msg)
		case modeTopicList:
			return m.handleTopicListKey(msg)
		case modeThread:
			return m.handleThreadKey(msg)
		case modeCompose:
			return m.handleComposeKey(msg)
		}
	}
	return m, nil
}

func (m *Boards) handleForumListKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		return m, nav.Navigate(nav.DestLobby)
	case "up", "k":
		if m.forumCursor > 0 {
			m.forumCursor--
		}
	case "down", "j":
		if m.forumCursor < len(m.forums)-1 {
			m.forumCursor++
		}
	case "enter":
		if len(m.forums) > 0 {
			return m, m.loadTopics(m.forums[m.forumCursor])
		}
	}
	return m, nil
}

func (m *Boards) handleTopicListKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.navSeq++ // invalidate any in-flight topics/posts load for this view
		m.mode = modeForumList
		m.activeForum = nil
		m.topics = nil
	case "up", "k":
		if m.topicCursor > 0 {
			m.topicCursor--
		}
	case "down", "j":
		if m.topicCursor < len(m.topics)-1 {
			m.topicCursor++
		}
	case "n":
		// New topic in the current forum. Title + body show together; Tab
		// moves focus between them.
		m.composeStage = stageNew
		m.composeFocus = 0
		m.composeErr = ""
		m.composeTitle.SetValue("")
		m.composeBody.SetValue("")
		m.composeTitle.Focus()
		m.composeBody.Blur()
		m.mode = modeCompose
		return m, textinput.Blink
	case "enter":
		if len(m.topics) > 0 {
			return m, m.loadPosts(m.topics[m.topicCursor])
		}
	}
	return m, nil
}

func (m *Boards) handleThreadKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.navSeq++ // invalidate any in-flight posts load so it can't yank us back
		m.mode = modeTopicList
		m.activeTopic = nil
		m.posts = nil
	case "up":
		if m.postScroll > 0 {
			m.postScroll--
		}
	case "down":
		m.postScroll++ // clamp in View() against total content
	case "k":
		if m.postScroll > 0 {
			m.postScroll--
		}
	case "j":
		m.postScroll++
	case "g":
		// Vim-style jump to top.
		m.postScroll = 0
	case "G":
		// Jump to bottom; View() clamps to maxScroll.
		m.postScroll = 1 << 20
	case "pgup":
		m.postScroll -= 10
		if m.postScroll < 0 {
			m.postScroll = 0
		}
	case "pgdown":
		m.postScroll += 10
	case "r":
		// Reply to the active topic.
		m.composeStage = stageReply
		m.composeErr = ""
		m.composeBody.SetValue("")
		m.mode = modeCompose
		return m, m.composeBody.Focus()
	}
	return m, nil
}

// handleComposeKey routes typing + submission through the title/body inputs.
// Esc cancels; Tab moves between title and body in stageNew (stageReply has
// no title and ignores Tab). Ctrl+S (and the convenience binding Ctrl+D)
// submit the form.
func (m *Boards) handleComposeKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.composeStage = stageNone
		m.composeErr = ""
		m.composeBody.Blur()
		if m.activeTopic != nil {
			m.mode = modeThread
		} else {
			m.mode = modeTopicList
		}
		return m, nil
	case "tab":
		if m.composeStage == stageNew {
			m.composeFocus = (m.composeFocus + 1) % 2
			if m.composeFocus == 0 {
				m.composeBody.Blur()
				m.composeTitle.Focus()
				return m, textinput.Blink
			}
			m.composeTitle.Blur()
			return m, m.composeBody.Focus()
		}
	case "ctrl+s", "ctrl+d":
		// Submit. Trim a final trailing newline (the textarea adds one after
		// the last visible row in some states) but preserve internal blank
		// lines.
		switch m.composeStage {
		case stageNew:
			title := strings.TrimSpace(m.composeTitle.Value())
			if title == "" {
				m.composeErr = "title required"
				m.composeFocus = 0
				m.composeBody.Blur()
				m.composeTitle.Focus()
				return m, textinput.Blink
			}
			body := strings.TrimRight(m.composeBody.Value(), "\n ")
			if strings.TrimSpace(body) == "" {
				m.composeErr = "body required"
				m.composeFocus = 1
				m.composeTitle.Blur()
				return m, m.composeBody.Focus()
			}
			return m, m.submitNewTopic(title, body)
		case stageReply:
			body := strings.TrimRight(m.composeBody.Value(), "\n ")
			if strings.TrimSpace(body) == "" {
				m.composeErr = "reply required"
				return m, nil
			}
			return m, m.submitReply(body)
		}
	}
	// Forward keystrokes to the focused input.
	var cmd tea.Cmd
	switch {
	case m.composeStage == stageNew && m.composeFocus == 0:
		m.composeTitle, cmd = m.composeTitle.Update(k)
	case m.composeStage == stageNew && m.composeFocus == 1:
		m.composeBody, cmd = m.composeBody.Update(k)
	case m.composeStage == stageReply:
		m.composeBody, cmd = m.composeBody.Update(k)
	}
	return m, cmd
}

func (m *Boards) submitNewTopic(title, body string) tea.Cmd {
	svc := m.sess.Forums
	user := m.sess.Identity
	forum := m.activeForum
	if forum == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		topic, err := svc.CreateTopic(ctx, nil, forum.ID, user.UserID, user.Handle, title, body)
		if err != nil {
			return boardsErrMsg{stage: "create topic", err: err}
		}
		posts, _ := svc.Posts(ctx, topic.ID)
		return boardsTopicCreatedMsg{topic: topic, posts: posts}
	}
}

func (m *Boards) submitReply(body string) tea.Cmd {
	svc := m.sess.Forums
	user := m.sess.Identity
	if m.activeTopic == nil {
		return nil
	}
	topicID := m.activeTopic.ID
	forumID := m.activeTopic.ForumID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		if _, err := svc.Reply(ctx, forumID, topicID, user.UserID, body); err != nil {
			return boardsErrMsg{stage: "reply", err: err}
		}
		return boardsPostCreatedMsg{}
	}
}

type boardsTopicCreatedMsg struct {
	topic realtime.Topic
	posts []realtime.Post
}
type boardsPostCreatedMsg struct{}

//
// view
//

var (
	// Post-card sub-styles. The bordered card itself comes from theme.PostCard;
	// these are the chips and accents that decorate the inside.
	postAuthorStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	postTimeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted))
	postBodyDim       = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText))
	boardsHint        = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	boardsDim         = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	boardsErrStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	composeFieldLabel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccentDim))
)

// chromeRows is the number of vertical cells the screen reserves for its
// header (1) + status bar (1). Subtract from sess.Height to size the body.
const chromeRows = 2

func (m *Boards) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return theme.Title.Render("Boards") + "\n\n" + theme.Hint.Render("connecting…")
	}
	switch m.mode {
	case modeForumList:
		return m.viewForumList()
	case modeTopicList:
		return m.viewTopicList()
	case modeThread:
		return m.viewThread()
	case modeCompose:
		return m.viewCompose()
	}
	return ""
}

// bodyWidth is the printable width of the screen body. Boards has no
// sidebars so it's the full session width.
func (m *Boards) bodyWidth() int {
	if m.sess.Width < 20 {
		return 20
	}
	return m.sess.Width
}

// availableHeight is the row count the boards screen actually owns. Root
// reserves the bottom row for its persistent status bar (matching chat.go).
func (m *Boards) availableHeight() int {
	h := m.sess.Height - 1
	if h < 1 {
		return 1
	}
	return h
}

// contentHeight is availableHeight minus the screen's own chrome (header
// + status row). Used to size scroll regions and pad body content.
func (m *Boards) contentHeight() int {
	h := m.availableHeight() - chromeRows
	if h < 1 {
		return 1
	}
	return h
}

// chromeHeader builds the breadcrumb + hint row that sits at the top of
// every Boards mode. Crumbs are joined with " › " in the accent-dim color
// so the separator reads as a soft chevron between segments. The hint
// string uses [bracketed] tokens to mark key glyphs that should render as
// chips — e.g. "[↑/↓] select" — so each shortcut scans as a button rather
// than as one long italic sentence.
func (m *Boards) chromeHeader(crumbs []string, hint string) string {
	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorAccentDim)).
		Render(" › ")
	styled := make([]string, len(crumbs))
	for i, c := range crumbs {
		styled[i] = theme.Header.Render(c)
	}
	line := strings.Join(styled, sep)
	if hint != "" {
		line += "  " + renderHintRow(hint)
	}
	return line
}

// renderHintRow walks `hint` looking for [token] runs and renders them via
// theme.KeyChip; everything else renders as muted italic. The input may
// contain runs like "[↑/↓] select · [Enter] open · [Esc] lobby".
func renderHintRow(hint string) string {
	muted := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorMuted)).
		Italic(true)
	var out strings.Builder
	for {
		open := strings.IndexByte(hint, '[')
		if open < 0 {
			out.WriteString(muted.Render(hint))
			break
		}
		close := strings.IndexByte(hint[open:], ']')
		if close < 0 {
			out.WriteString(muted.Render(hint))
			break
		}
		close += open
		out.WriteString(muted.Render(hint[:open]))
		out.WriteString(theme.KeyChip(hint[open+1 : close]))
		hint = hint[close+1:]
	}
	return out.String()
}

// chromeStatus is the full-width status bar shown at the bottom of every
// mode. left is the contextual count ("12 forums · 3 unread"); right is
// the always-visible identity marker. Padded to exactly bodyWidth().
func (m *Boards) chromeStatus(left, right string) string {
	w := m.bodyWidth()
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return theme.StatusBar.Render(left + strings.Repeat(" ", gap) + right)
}

// statusIdentity is the right-hand chunk every status bar shows. Centralized
// so an identity change (e.g., switching handles in a future sysop /assume)
// flips every screen at once. Sysops get a tiny crown glyph as a passive
// role reminder so the moderation context is always visible.
func (m *Boards) statusIdentity() string {
	if m.sess.Identity.IsSysop {
		return "♛ @" + m.sess.Identity.Handle
	}
	return "@" + m.sess.Identity.Handle
}

// viewForumList renders the top-level forum list with chrome.
func (m *Boards) viewForumList() string {
	header := m.chromeHeader([]string{"Boards"}, "[↑/↓] select · [Enter] open · [Esc] lobby")
	body := m.renderForumRows()
	body = padToHeight(body, m.contentHeight())
	totalUnread := 0
	for _, n := range m.unreadByForum {
		totalUnread += n
	}
	left := fmt.Sprintf("%d %s", len(m.forums), plural("forum", len(m.forums)))
	if totalUnread > 0 {
		left += " · " + theme.UnreadDot.Render("●") + boardsDim.Render(fmt.Sprintf(" %d unread", totalUnread))
	}
	status := m.chromeStatus(left, m.statusIdentity())
	return lipgloss.JoinVertical(lipgloss.Left, header, body, status)
}

// renderForumRows produces the body region for the forum list — error
// banner (if any), then one multi-line block per forum, then an empty-state
// placeholder when nothing is loaded. Each forum's block is icon + text,
// 4 rows tall, separated by a blank row.
func (m *Boards) renderForumRows() string {
	var b strings.Builder
	if m.err != "" {
		b.WriteString(boardsErrStyle.Render("! " + m.err))
		b.WriteString("\n")
	}
	if len(m.forums) == 0 {
		b.WriteString(renderEmptyStateFrame("no forums yet — sysop should seed one"))
		return b.String()
	}
	for i, f := range m.forums {
		b.WriteString(m.formatForumBlock(f, i == m.forumCursor))
		if i < len(m.forums)-1 {
			b.WriteString("\n\n")
		} else {
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// forumIconHeight is the row count of each forum block. We render only 3
// content rows (header + description + gutter) and clip the 4-row icon art
// down to match — the empty 4th row was wasting vertical space and limiting
// how many forums fit on an 80×24 session to ~3.
const forumIconHeight = 3

// forumIconColWidth is the total width reserved on the left of every text
// block: icon glyph (currently 9 cols) + a 2-col gap before the text starts.
const forumIconColWidth = 11

// formatForumBlock renders one forum as a 3-row stack: accent bar (or blank)
// + icon column + text column. The selected forum carries a colored left-
// edge bar instead of the old ▸ glyph so the cursor reads as a continuous
// stripe across every row.
func (m *Boards) formatForumBlock(f realtime.Forum, active bool) string {
	iconLines := m.forumIconLines(f)
	textLines := m.forumTextLines(f, active)
	var b strings.Builder
	for i := 0; i < forumIconHeight; i++ {
		var gutter string
		if active {
			gutter = theme.AccentBar.Render(" ") + " "
		} else {
			gutter = "  "
		}
		b.WriteString(gutter + iconLines[i] + textLines[i])
		if i < forumIconHeight-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// forumIconLines returns the icon glyph as forumIconHeight strings, each
// padded to forumIconColWidth so the text column always starts at the same
// X. A nil session.BoardIcons or a missing glyph yields blank padding rather
// than a hard error — the forum row still renders, just without artwork.
func (m *Boards) forumIconLines(f realtime.Forum) []string {
	out := make([]string, forumIconHeight)
	var grid *art.CellGrid
	if m.sess.BoardIcons != nil {
		grid = m.sess.BoardIcons.Get(slugifyForumName(f.Name))
	}
	if grid == nil {
		blank := strings.Repeat(" ", forumIconColWidth)
		for i := range out {
			out[i] = blank
		}
		return out
	}
	lines := strings.Split(components.RenderCellGrid(grid), "\n")
	for i := 0; i < forumIconHeight; i++ {
		if i >= len(lines) {
			out[i] = strings.Repeat(" ", forumIconColWidth)
			continue
		}
		line := lines[i]
		pad := forumIconColWidth - lipgloss.Width(line)
		if pad < 0 {
			pad = 0
		}
		out[i] = line + strings.Repeat(" ", pad)
	}
	return out
}

// forumTextLines composes the 3-row text column for one forum. Row 1 is the
// name + topic count + age + optional unread badge; row 2 the description
// (italic + muted for quiet forums); row 3 is a small bottom gutter so
// adjacent blocks don't collide visually. When active, every row is wrapped
// in theme.RowHighlight so the selection background runs the full width of
// the text column.
func (m *Boards) forumTextLines(f realtime.Forum, active bool) []string {
	textWidth := m.bodyWidth() - 2 - forumIconColWidth
	if textWidth < 20 {
		textWidth = 20
	}
	name := "#" + f.Name
	metaText := fmt.Sprintf("%d %s · %s",
		f.TopicCount, plural("topic", int(f.TopicCount)),
		components.FormatRelativeAge(f.LastActivityAt))
	meta := boardsDim.Render(metaText)
	if n := m.unreadByForum[f.ID]; n > 0 {
		meta = meta + " " + theme.UnreadBadge.Render(fmt.Sprintf("%d new", n))
	}
	row1 := joinLR(name, meta, textWidth)

	descStyle := boardsDim
	if f.TopicCount == 0 {
		descStyle = lipgloss.NewStyle().
			Italic(true).
			Foreground(lipgloss.Color(theme.ColorMuted))
	}
	desc := descStyle.Render(f.Description)
	row2 := joinLR(desc, "", textWidth)
	row3 := strings.Repeat(" ", textWidth)
	lines := []string{row1, row2, row3}
	if active {
		for i, ln := range lines {
			lines[i] = theme.RowHighlight.Render(ln)
		}
	}
	return lines
}

// slugifyForumName lowercases the forum name, maps spaces and underscores
// to "-", and drops everything except [a-z0-9-]. The result is the lookup
// key against board-icons/*.ans; misses fall back to "default" in the
// FileSystemBoardIcons provider.
func slugifyForumName(name string) string {
	s := strings.ToLower(name)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == ' ' || r == '_':
			b.WriteRune('-')
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// viewTopicList renders the topic list for the active forum.
func (m *Boards) viewTopicList() string {
	header := m.chromeHeader(
		[]string{"Boards", "#" + m.activeForum.Name},
		"[↑/↓] select · [Enter] open · [n] new · [Esc] back",
	)
	body := m.renderTopicRows()
	body = padToHeight(body, m.contentHeight())
	unread := 0
	for _, n := range m.unreadByTopic {
		unread += n
	}
	left := fmt.Sprintf("%d %s", len(m.topics), plural("topic", len(m.topics)))
	if unread > 0 {
		left += " · " + theme.UnreadDot.Render("●") + boardsDim.Render(fmt.Sprintf(" %d unread", unread))
	}
	status := m.chromeStatus(left, m.statusIdentity())
	return lipgloss.JoinVertical(lipgloss.Left, header, body, status)
}

func (m *Boards) renderTopicRows() string {
	var b strings.Builder
	if m.err != "" {
		b.WriteString(boardsErrStyle.Render("! " + m.err))
		b.WriteString("\n")
	}
	if len(m.topics) == 0 {
		b.WriteString(renderEmptyStateFrame("no topics in this forum yet — press n to start one"))
		return b.String()
	}
	for i, t := range m.topics {
		unread := m.unreadByTopic[t.ID] > 0
		dot := theme.ReadDot.Render("○")
		if unread {
			dot = theme.UnreadDot.Render("●")
		}
		authorChip := theme.AuthorChip.Render("@" + t.AuthorHandle)
		titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText)).Bold(unread)
		if !unread {
			titleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
		}
		titleWidth := m.bodyWidth() - 60
		if titleWidth < 20 {
			titleWidth = 20
		}
		title := titleStyle.Render(runewidth.Truncate(t.Title, titleWidth, "…"))
		activity := postActivityBar(int(t.PostCount))
		meta := boardsDim.Render(fmt.Sprintf(
			"%d %s · %s",
			t.PostCount, plural("post", int(t.PostCount)),
			components.FormatRelativeAge(t.LastPostAt),
		))
		left := dot + " " + authorChip + " " + title
		right := activity + "  " + meta
		row := joinLR(left, right, m.bodyWidth()-4)
		if i == m.topicCursor {
			b.WriteString("▸ " + theme.RowHighlight.Render(row))
		} else {
			b.WriteString("  " + row)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// postActivityBar maps a post count to one of eight block characters so
// the user can compare topic engagement at a glance. The buckets are
// chosen so 1-2 posts sit in the small range and 30+ posts saturate.
func postActivityBar(n int) string {
	if n <= 0 {
		return " "
	}
	bars := []rune{'▏', '▎', '▍', '▌', '▋', '▊', '▉', '█'}
	bucket := 0
	switch {
	case n >= 30:
		bucket = 7
	case n >= 20:
		bucket = 6
	case n >= 12:
		bucket = 5
	case n >= 7:
		bucket = 4
	case n >= 4:
		bucket = 3
	case n >= 2:
		bucket = 2
	case n >= 1:
		bucket = 1
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorAccent)).
		Render(string(bars[bucket]))
}

// renderEmptyStateFrame wraps a short message in a small dim box so the
// user reads the state as deliberate rather than as a bug. Used by the
// empty forum / topic list / thread states.
func renderEmptyStateFrame(message string) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(theme.ColorDim)).
		Foreground(lipgloss.Color(theme.ColorAccentDim)).
		Italic(true).
		Padding(0, 2).
		Render(message)
}

// viewThread renders the active topic as a scrollable stack of bordered
// post cards. Scroll model is line-aware-within-cards: cards are pre-
// rendered, joined, then we slice into the resulting flat line buffer to
// honor the existing line-step scroll keys. Per the design decision, a
// card straddling the viewport edge clips with its border partly visible
// rather than snap-scrolling.
func (m *Boards) viewThread() string {
	header := m.chromeHeader(
		[]string{"Boards", "#" + m.activeForum.Name, runewidth.Truncate(m.activeTopic.Title, 60, "…")},
		"[↑/↓] scroll · [PgUp/PgDn] page · [g/G] top/bottom · [r] reply · [Esc] back",
	)

	var body string
	maxScroll := 0
	if m.err != "" {
		body = boardsErrStyle.Render("! "+m.err) + "\n"
	}
	if len(m.posts) == 0 {
		body += boardsHint.Render("(empty thread)")
	} else {
		cardWidth := m.bodyWidth() - 4
		if cardWidth < 30 {
			cardWidth = 30
		}
		var lines []string
		ownID := m.sess.Identity.UserID
		for i, p := range m.posts {
			card := m.renderPostCard(p, i, i == 0, p.CreatedByID == ownID, cardWidth)
			lines = append(lines, strings.Split(card, "\n")...)
			lines = append(lines, "") // gap between cards
		}
		// Clamp scroll against the flattened line buffer. Recompute and
		// surface maxScroll for the status bar so the user can see how far
		// they are through the thread.
		availH := m.contentHeight()
		maxScroll = len(lines) - availH
		if maxScroll < 0 {
			maxScroll = 0
		}
		if m.postScroll > maxScroll {
			m.postScroll = maxScroll
		}
		end := m.postScroll + availH
		if end > len(lines) {
			end = len(lines)
		}
		body += strings.Join(lines[m.postScroll:end], "\n")
	}
	body = padToHeight(body, m.contentHeight())

	left := fmt.Sprintf("%d %s", len(m.posts), plural("post", len(m.posts)))
	if maxScroll > 0 {
		left += fmt.Sprintf(" · %d/%d", m.postScroll, maxScroll)
	}
	status := m.chromeStatus(left, m.statusIdentity())
	return lipgloss.JoinVertical(lipgloss.Left, header, body, status)
}

// renderPostCard wraps one post in theme.PostCard. Layout inside the card:
//
//	#3  @handle  Jan 2 15:04             [OP] [SYSOP] (edited)
//
//	  post body, indented 2 cells, wrapped to fit…
//
// isOP is true for the topic's root post — gets an OP chip in the header.
// isMine highlights the card border in the brighter accent color so the user
// can find their own posts at a glance in long threads.
func (m *Boards) renderPostCard(p realtime.Post, index int, isOP, isMine bool, cardWidth int) string {
	// Inside the card, the usable text width is cardWidth - 2 (border) - 2
	// (padding). Wrap body lines to that, then indent the body by 2 so it
	// sits visually distinct from the header line.
	innerW := cardWidth - 4
	if innerW < 20 {
		innerW = 20
	}
	number := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorAccentDim)).
		Render(fmt.Sprintf("#%d", index+1))
	left := number + "  " + postAuthorStyle.Render("@"+p.AuthorHandle) + "  " + postTimeStyle.Render(m.sess.DisplayPrefs.FormatDateTime(p.CreatedAt))
	var chips []string
	if isOP {
		chips = append(chips, theme.OPChip.Render("OP"))
	}
	if p.AuthorIsSysop {
		chips = append(chips, theme.SysopChip.Render("SYSOP"))
	}
	if !p.EditedAt.IsZero() {
		chips = append(chips, theme.EditedChip.Render("(edited)"))
	}
	right := strings.Join(chips, " ")
	headerLine := joinLR(left, right, innerW)

	wrapped := wrapToWidth(p.Body, innerW-2)
	indented := make([]string, len(wrapped))
	for i, ln := range wrapped {
		indented[i] = "  " + ln
	}
	bodyLines := strings.Join(indented, "\n")
	inner := headerLine + "\n\n" + postBodyDim.Render(bodyLines)

	card := theme.PostCard
	if isMine {
		card = card.BorderForeground(lipgloss.Color(theme.ColorAccent))
	}
	return card.Width(cardWidth).Render(inner)
}

// viewCompose renders the compose form as a centered modal over the dimmed
// underlying scene. The base is whichever view the user came from
// (viewThread for reply, viewTopicList for new-topic-in-forum). The dim
// transform is SGR-aware via components.DimSGR so accents fade too.
func (m *Boards) viewCompose() string {
	// Base scene — render whichever underlying view the user came from,
	// then dim it as a single string so cells inside the modal region get
	// overwritten cleanly.
	var base string
	if m.activeTopic != nil {
		// Reply: dim the thread underneath so the modal carries focus.
		base = m.viewThread()
	} else if m.activeForum != nil {
		base = m.viewTopicList()
	}
	base = components.DimSGR(base, theme.ColorDim)

	modal := m.renderComposeModal()
	return components.Overlay(base, modal, m.bodyWidth(), m.availableHeight())
}

// renderComposeModal builds the modal box (without overlay positioning).
// Centralizes the layout so the test path and the production path agree.
func (m *Boards) renderComposeModal() string {
	// Modal width: most of the screen, capped at 80 so 4K SSH sessions
	// don't render a 200-wide compose box. Width includes border + padding,
	// so the textarea is sized to fit inside.
	modalW := m.bodyWidth() - 10
	if modalW > 80 {
		modalW = 80
	}
	if modalW < 40 {
		modalW = 40
	}
	innerW := modalW - 6 // border(2) + padding(4 from Padding(1,2))
	if innerW < 20 {
		innerW = 20
	}
	// Resize the textarea live so the modal stays proportional regardless
	// of how the user resized their terminal between compose sessions.
	m.composeBody.SetWidth(innerW)
	m.composeTitle.Width = innerW - 8 // "title: " + space

	var b strings.Builder
	switch m.composeStage {
	case stageNew:
		b.WriteString(theme.Header.Render("new topic in #" + m.activeForum.Name))
	case stageReply:
		title := runewidth.Truncate(m.activeTopic.Title, innerW-12, "…")
		b.WriteString(theme.Header.Render("reply to: " + title))
	}
	b.WriteString("\n")
	switch m.composeStage {
	case stageNew:
		b.WriteString(boardsHint.Render("Tab move · Enter newline · Ctrl+S submit · Esc cancel"))
	case stageReply:
		b.WriteString(boardsHint.Render("Enter newline · Ctrl+S submit · Esc cancel"))
	}
	b.WriteString("\n\n")
	if m.composeErr != "" {
		b.WriteString(boardsErrStyle.Render("! " + m.composeErr))
		b.WriteString("\n\n")
	}
	if m.composeStage == stageNew {
		titleLabel := composeFieldLabel.Render("title")
		if m.composeFocus == 0 {
			titleLabel = lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color(theme.ColorAccent)).Render("title")
		}
		b.WriteString(titleLabel + "  " + m.composeTitle.View())
		b.WriteString("\n\n")
		bodyLabel := composeFieldLabel.Render("body")
		if m.composeFocus == 1 {
			bodyLabel = lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color(theme.ColorAccent)).Render("body")
		}
		b.WriteString(bodyLabel + "\n")
	}
	b.WriteString(m.composeBody.View())
	// Char counter footer. Color-codes by usage band so the user knows when
	// they're approaching the 4000-char cap without counting digits.
	count := len(m.composeBody.Value())
	const cap = 4000
	pct := float64(count) / float64(cap)
	counterColor := theme.ColorDim
	switch {
	case pct >= 0.9:
		counterColor = theme.ColorRed
	case pct >= 0.7:
		counterColor = theme.ColorYellow
	}
	counter := lipgloss.NewStyle().
		Foreground(lipgloss.Color(counterColor)).
		Render(fmt.Sprintf("%d / %d", count, cap))
	submitChip := lipgloss.NewStyle().Bold(true).
		Background(lipgloss.Color(theme.ColorSurfaceAlt)).
		Foreground(lipgloss.Color(theme.ColorYellow)).
		Padding(0, 1).Render("[ Ctrl+S submit ]")
	b.WriteString("\n")
	b.WriteString(joinLR(counter, submitChip, innerW))

	return theme.ModalFrame.Width(modalW).Render(b.String())
}

// joinLR returns a single row of width w with `left` left-aligned and
// `right` right-aligned. If the two pieces collide, the right side wins
// and the left is truncated. Width counts visible cells (lipgloss.Width).
func joinLR(left, right string, w int) string {
	if w <= 0 {
		return left
	}
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	gap := w - lw - rw
	if gap < 1 {
		// Truncate left to make room — keep the right anchor visible.
		keep := w - rw - 1
		if keep < 0 {
			keep = 0
		}
		left = runewidth.Truncate(ansiStripForTrunc(left), keep, "…")
		return left + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

// ansiStripForTrunc removes ANSI escapes so runewidth.Truncate doesn't
// count SGR bytes as visible cells. Used only when the row needs to be
// trimmed to fit the available width.
func ansiStripForTrunc(s string) string {
	// We never wrap user-typed content in SGR in this screen, so a naive
	// scan for the CSI prefix is fine; lipgloss-rendered chunks all carry
	// "\x1b[…m" sequences which this strips out.
	if !strings.Contains(s, "\x1b") {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == 0x1b && s[i+1] == '[' {
			end := strings.IndexAny(s[i:], "mABCDEFGHJKSTfnsulhij")
			if end >= 0 {
				i += end + 1
				continue
			}
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// padToHeight pads s with empty rows until it has at least h rows. The
// final row is not truncated — callers should already size content to fit.
func padToHeight(s string, h int) string {
	cur := strings.Count(s, "\n") + 1
	if s == "" {
		cur = 0
	}
	if cur >= h {
		return s
	}
	return s + strings.Repeat("\n", h-cur)
}

//
// helpers
//

// wrapToWidth is a tiny word-wrap. Forum bodies are plain text, no SGR yet.
func wrapToWidth(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			out = append(out, "")
			continue
		}
		line := ""
		lineW := 0
		for _, word := range strings.Fields(paragraph) {
			ww := runewidth.StringWidth(word)
			switch {
			case line == "":
				line, lineW = word, ww
			case lineW+1+ww <= width:
				line += " " + word
				lineW += 1 + ww
			default:
				out = append(out, line)
				line, lineW = word, ww
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}
