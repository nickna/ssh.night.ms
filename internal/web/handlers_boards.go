package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nickna/ssh.night.ms/internal/realtime"
)

// Boards is the server-rendered web view of the forum feature. It reuses the
// same realtime.ForumService the SSH/TUI path uses (via h.deps.Forums), so the
// two surfaces share one source of truth — reading a thread here marks it read
// over SSH too. Reading is public; posting (new topic / reply) requires a
// session and redirects anonymous users to /login.

const (
	// topicsPerPage / postsPerPage size the offset pagination. Kept small so
	// each page is a single screenful and the offset queries stay cheap.
	topicsPerPage = 25
	postsPerPage  = 20

	// boardTitleMax mirrors the topics.title varchar(120) column; boardBodyMax
	// mirrors the TUI compose cap (boards.go composeBody CharLimit = 4000).
	boardTitleMax = 120
	boardBodyMax  = 4000
)

// pageNav is the pagination view-model handed to the topic/forum templates.
// BasePath is the path without query string (e.g. "/boards/5"); the template
// appends ?page=N to build prev/next links.
type pageNav struct {
	CurrentPage int
	TotalPages  int
	HasPrev     bool
	HasNext     bool
	PrevPage    int
	NextPage    int
	BasePath    string
}

// newPageNav clamps the requested page into [1, totalPages] and derives the
// prev/next state. perPage must be > 0. total is the full row count, taken
// from the denormalized counters (forum.TopicCount / topic.PostCount) so no
// COUNT query is needed.
func newPageNav(page, perPage int, total int64, basePath string) pageNav {
	totalPages := int((total + int64(perPage) - 1) / int64(perPage))
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	return pageNav{
		CurrentPage: page,
		TotalPages:  totalPages,
		HasPrev:     page > 1,
		HasNext:     page < totalPages,
		PrevPage:    page - 1,
		NextPage:    page + 1,
		BasePath:    basePath,
	}
}

// parsePage reads the ?page= query param, defaulting to 1 on absent/invalid.
func parsePage(r *http.Request) int {
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		return p
	}
	return 1
}

//
// Forum list — GET /boards
//

type forumListItem struct {
	ID             int64
	Name           string
	Description    string
	TopicCount     int32
	LastActivityAt time.Time
	Unread         int
}

type boardsIndexData struct {
	pageData
	Forums []forumListItem
}

func (h *handlers) boardsIndex(w http.ResponseWriter, r *http.Request) {
	forums, err := h.deps.Forums.ListForums(r.Context())
	if err != nil {
		h.deps.Logger.Error("boards: list forums", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Overlay per-forum unread badges when signed in. Best-effort: a failure
	// here just drops the badges, it doesn't block the page.
	var unread map[int64]int
	if id := identityFrom(r); id != nil {
		if u, err := h.deps.Forums.UnreadCountsByForum(r.Context(), id.UserID); err == nil {
			unread = u
		} else {
			h.deps.Logger.Warn("boards: unread by forum", "user_id", id.UserID, "err", err)
		}
	}
	items := make([]forumListItem, 0, len(forums))
	for _, f := range forums {
		items = append(items, forumListItem{
			ID:             f.ID,
			Name:           f.Name,
			Description:    f.Description,
			TopicCount:     f.TopicCount,
			LastActivityAt: f.LastActivityAt,
			Unread:         unread[f.ID],
		})
	}
	h.renderProfile(w, "boards_index", boardsIndexData{
		pageData: h.basePage(r, "boards"),
		Forums:   items,
	})
}

//
// Topic list — GET /boards/{forumID}
//

type forumHeader struct {
	ID          int64
	Name        string
	Description string
}

type topicListItem struct {
	ID           int64
	Title        string
	AuthorHandle string
	PostCount    int64
	LastPostAt   time.Time
	Unread       int
}

type boardForumData struct {
	pageData
	Forum  forumHeader
	Topics []topicListItem
	Nav    pageNav
}

func (h *handlers) boardForum(w http.ResponseWriter, r *http.Request) {
	forumID, ok := parseID(r, "forumID")
	if !ok {
		http.NotFound(w, r)
		return
	}
	forum, err := h.deps.Forums.GetForum(r.Context(), forumID)
	if err != nil {
		if errors.Is(err, realtime.ErrForumNotFound) {
			http.NotFound(w, r)
			return
		}
		h.deps.Logger.Error("boards: get forum", "forum", forumID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	basePath := "/boards/" + strconv.FormatInt(forumID, 10)
	nav := newPageNav(parsePage(r), topicsPerPage, int64(forum.TopicCount), basePath)
	offset := int32((nav.CurrentPage - 1) * topicsPerPage)

	topics, err := h.deps.Forums.TopicsPage(r.Context(), forumID, topicsPerPage, offset)
	if err != nil {
		h.deps.Logger.Error("boards: topics page", "forum", forumID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var unread map[int64]int
	if id := identityFrom(r); id != nil {
		if u, err := h.deps.Forums.UnreadTopicCounts(r.Context(), id.UserID, forumID); err == nil {
			unread = u
		} else {
			h.deps.Logger.Warn("boards: unread topic counts", "forum", forumID, "err", err)
		}
	}

	items := make([]topicListItem, 0, len(topics))
	for _, t := range topics {
		items = append(items, topicListItem{
			ID:           t.ID,
			Title:        t.Title,
			AuthorHandle: t.AuthorHandle,
			PostCount:    t.PostCount,
			LastPostAt:   t.LastPostAt,
			Unread:       unread[t.ID],
		})
	}
	h.renderProfile(w, "boards_forum", boardForumData{
		pageData: h.basePage(r, forum.Name),
		Forum:    forumHeader{ID: forum.ID, Name: forum.Name, Description: forum.Description},
		Topics:   items,
		Nav:      nav,
	})
}

//
// Thread — GET /boards/{forumID}/{topicID}
//

type topicHeader struct {
	ID      int64
	ForumID int64
	Title   string
}

type postItem struct {
	AuthorHandle string
	Body         string
	CreatedAt    time.Time
	IsOP         bool
	IsSysop      bool
	Edited       bool
}

type boardTopicData struct {
	pageData
	Forum    forumHeader
	Topic    topicHeader
	Posts    []postItem
	Nav      pageNav
	CanReply bool
	Notice   string
}

func (h *handlers) boardTopic(w http.ResponseWriter, r *http.Request) {
	forumID, ok := parseID(r, "forumID")
	if !ok {
		http.NotFound(w, r)
		return
	}
	topicID, ok := parseID(r, "topicID")
	if !ok {
		http.NotFound(w, r)
		return
	}
	topic, err := h.deps.Forums.GetTopic(r.Context(), topicID)
	if err != nil {
		if errors.Is(err, realtime.ErrTopicNotFound) {
			http.NotFound(w, r)
			return
		}
		h.deps.Logger.Error("boards: get topic", "topic", topicID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The forumID in the URL must match the topic's actual forum so links are
	// canonical and we never render a topic under the wrong forum breadcrumb.
	if topic.ForumID != forumID {
		http.NotFound(w, r)
		return
	}
	forum, err := h.deps.Forums.GetForum(r.Context(), forumID)
	if err != nil {
		if errors.Is(err, realtime.ErrForumNotFound) {
			http.NotFound(w, r)
			return
		}
		h.deps.Logger.Error("boards: get forum", "forum", forumID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	basePath := "/boards/" + strconv.FormatInt(forumID, 10) + "/" + strconv.FormatInt(topicID, 10)
	nav := newPageNav(parsePage(r), postsPerPage, topic.PostCount, basePath)
	offset := int32((nav.CurrentPage - 1) * postsPerPage)

	posts, err := h.deps.Forums.PostsPage(r.Context(), topicID, postsPerPage, offset)
	if err != nil {
		h.deps.Logger.Error("boards: posts page", "topic", topicID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	id := identityFrom(r)
	if id != nil {
		// Fire-and-forget read-marker. Shared with the SSH path so reading
		// here clears the unread badge over SSH too.
		if err := h.deps.Forums.TouchTopicRead(r.Context(), id.UserID, topicID); err != nil {
			h.deps.Logger.Warn("boards: touch read", "topic", topicID, "err", err)
		}
	}

	items := make([]postItem, 0, len(posts))
	for _, p := range posts {
		items = append(items, postItem{
			AuthorHandle: p.AuthorHandle,
			Body:         p.Body,
			CreatedAt:    p.CreatedAt,
			IsOP:         p.CreatedByID == topic.CreatedByID,
			IsSysop:      p.AuthorIsSysop,
			Edited:       !p.EditedAt.IsZero(),
		})
	}

	notice := ""
	switch r.URL.Query().Get("err") {
	case "empty":
		notice = "reply can't be empty"
	case "toolong":
		notice = "reply too long (4000 characters max)"
	}

	h.renderProfile(w, "boards_topic", boardTopicData{
		pageData: h.basePage(r, topic.Title),
		Forum:    forumHeader{ID: forum.ID, Name: forum.Name},
		Topic:    topicHeader{ID: topic.ID, ForumID: topic.ForumID, Title: topic.Title},
		Posts:    items,
		Nav:      nav,
		CanReply: id != nil,
		Notice:   notice,
	})
}

//
// New topic — GET/POST /boards/{forumID}/new
//

type boardNewData struct {
	pageData
	Forum forumHeader
	Title string
	Body  string
	Error string
}

func (h *handlers) boardNewGet(w http.ResponseWriter, r *http.Request) {
	if identityFrom(r) == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	forumID, ok := parseID(r, "forumID")
	if !ok {
		http.NotFound(w, r)
		return
	}
	forum, err := h.deps.Forums.GetForum(r.Context(), forumID)
	if err != nil {
		if errors.Is(err, realtime.ErrForumNotFound) {
			http.NotFound(w, r)
			return
		}
		h.deps.Logger.Error("boards: new get forum", "forum", forumID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.renderProfile(w, "boards_new", boardNewData{
		pageData: h.basePage(r, "new topic"),
		Forum:    forumHeader{ID: forum.ID, Name: forum.Name},
	})
}

func (h *handlers) boardNewPost(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	forumID, ok := parseID(r, "forumID")
	if !ok {
		http.NotFound(w, r)
		return
	}
	forum, err := h.deps.Forums.GetForum(r.Context(), forumID)
	if err != nil {
		if errors.Is(err, realtime.ErrForumNotFound) {
			http.NotFound(w, r)
			return
		}
		h.deps.Logger.Error("boards: new post forum", "forum", forumID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(r.PostFormValue("title"))
	body := strings.TrimSpace(r.PostFormValue("body"))

	reRender := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		h.renderProfile(w, "boards_new", boardNewData{
			pageData: h.basePage(r, "new topic"),
			Forum:    forumHeader{ID: forum.ID, Name: forum.Name},
			Title:    title,
			Body:     body,
			Error:    msg,
		})
	}
	switch {
	case title == "":
		reRender("title is required")
		return
	case len([]rune(title)) > boardTitleMax:
		reRender("title too long (120 characters max)")
		return
	case body == "":
		reRender("post body is required")
		return
	case len([]rune(body)) > boardBodyMax:
		reRender("post too long (4000 characters max)")
		return
	}

	topic, err := h.deps.Forums.CreateTopic(r.Context(), nil, forumID, id.UserID, id.Handle, title, body)
	if err != nil {
		h.deps.Logger.Error("boards: create topic", "forum", forumID, "err", err)
		reRender("could not create topic — try again")
		return
	}
	http.Redirect(w, r, "/boards/"+strconv.FormatInt(forumID, 10)+"/"+strconv.FormatInt(topic.ID, 10), http.StatusSeeOther)
}

//
// Reply — POST /boards/{forumID}/{topicID}/reply
//

func (h *handlers) boardReplyPost(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	forumID, ok := parseID(r, "forumID")
	if !ok {
		http.NotFound(w, r)
		return
	}
	topicID, ok := parseID(r, "topicID")
	if !ok {
		http.NotFound(w, r)
		return
	}
	topic, err := h.deps.Forums.GetTopic(r.Context(), topicID)
	if err != nil {
		if errors.Is(err, realtime.ErrTopicNotFound) {
			http.NotFound(w, r)
			return
		}
		h.deps.Logger.Error("boards: reply get topic", "topic", topicID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if topic.ForumID != forumID {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	body := strings.TrimSpace(r.PostFormValue("body"))
	topicPath := "/boards/" + strconv.FormatInt(forumID, 10) + "/" + strconv.FormatInt(topicID, 10)

	// Validation failures bounce back to the thread with a flash error rather
	// than re-rendering the whole paginated thread inline.
	if body == "" {
		http.Redirect(w, r, topicPath+"?err=empty", http.StatusSeeOther)
		return
	}
	if len([]rune(body)) > boardBodyMax {
		http.Redirect(w, r, topicPath+"?err=toolong", http.StatusSeeOther)
		return
	}

	if _, err := h.deps.Forums.Reply(r.Context(), forumID, topicID, id.UserID, body); err != nil {
		h.deps.Logger.Error("boards: reply", "topic", topicID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Land on the last page so the new reply is visible. After this reply the
	// thread holds PostCount+1 posts.
	lastPage := int((topic.PostCount + 1 + int64(postsPerPage) - 1) / int64(postsPerPage))
	if lastPage < 1 {
		lastPage = 1
	}
	http.Redirect(w, r, topicPath+"?page="+strconv.Itoa(lastPage), http.StatusSeeOther)
}

// parseID pulls a chi URL param and parses it as a positive int64. The bool is
// false on a missing/non-numeric/non-positive value so callers can 404.
func parseID(r *http.Request, name string) (int64, bool) {
	v, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}
