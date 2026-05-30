// Package onenote is the per-user OneNote (Microsoft Graph) read/edit service.
// It is a process-singleton bag in session.Deps, consumed directly in-process
// by the SSH TUI (like the news/weather providers) and wrapped by the web
// REST handlers under /api/onenote — both go through this one Service so the
// two surfaces can't drift.
//
// Microsoft Graph is the source of truth. This package keeps only a thin cache:
// an in-process ttlcache for hot reads plus a small Postgres layer (recent
// pages + a short-TTL page-content snapshot + per-user prefs) for
// cross-restart survival. It never mirrors a user's full notebook tree.
//
// Auth flows through internal/auth/usertoken: every call resolves a valid
// Microsoft access token (refreshing on demand) and requires the
// Notes.ReadWrite scope. The typed usertoken errors (ErrNoLink,
// ErrMissingScope, ErrNeedsReauth) bubble out unchanged so callers render the
// right "link" / "re-authorize" CTA instead of a 500.
package onenote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/providers/ttlcache"
)

// ErrConfirmRequired is returned by ReplaceBody when a full-page rewrite would
// drop non-text content (images/tables/attachments) and the caller hasn't set
// confirmReplace. The TUI/REST surfaces a "this will drop images/tables —
// confirm?" prompt and retries with confirm=true.
var ErrConfirmRequired = errors.New("onenote: confirmation required to overwrite non-text content")

// TokenSource resolves a valid provider access token for a user. *usertoken.
// Source satisfies it; tests inject a fake. Decoupling here keeps the Service
// testable without a real sealer/DB.
type TokenSource interface {
	Token(ctx context.Context, userID int64, requiredScopes ...string) (string, error)
}

const (
	recentPageLimit = 50  // Graph $top for page listings
	recentKeepRows  = 100 // cap on per-user onenote_recent_pages rows
)

// Config constructs a Service. Tokens is required; everything else has a
// sensible default. Queries may be nil (the Postgres cache layer is then
// skipped — useful in tests). HTTPDo overrides the HTTP client for tests.
type Config struct {
	Tokens     TokenSource
	Queries    *gen.Queries
	HTTPClient *http.Client
	HTTPDo     func(*http.Request) (*http.Response, error)
	BaseURL    string
	Logger     *slog.Logger
	ListTTL    time.Duration
	ContentTTL time.Duration
}

// Service is the OneNote facade. Safe for concurrent use.
type Service struct {
	tokens  TokenSource
	queries *gen.Queries
	logger  *slog.Logger
	baseURL string
	httpDo  func(*http.Request) (*http.Response, error)

	notebookCache *ttlcache.Cache[listKey, []Notebook]
	sectionCache  *ttlcache.Cache[listKey, []Section]
	pageListCache *ttlcache.Cache[listKey, []Page]
	pageCache     *ttlcache.Cache[pageKey, *PageContent]
}

type listKey struct {
	userID int64
	kind   string // "notebooks" | "sections" | "pages"
	parent string // notebook/section id, or "" for the top-level/recent list
}

type pageKey struct {
	userID int64
	pageID string
}

// New builds a Service from cfg. Listings cache with StaleOnError (a transient
// Graph blip shows the last-known tree rather than a blank screen); page
// content does not, so edit/refresh errors surface.
func New(cfg Config) *Service {
	listTTL := cfg.ListTTL
	if listTTL <= 0 {
		listTTL = 5 * time.Minute
	}
	contentTTL := cfg.ContentTTL
	if contentTTL <= 0 {
		contentTTL = 60 * time.Second
	}
	base := cfg.BaseURL
	if base == "" {
		base = graphBaseURL
	}
	httpDo := cfg.HTTPDo
	if httpDo == nil {
		client := cfg.HTTPClient
		if client == nil {
			client = http.DefaultClient
		}
		httpDo = client.Do
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		tokens:        cfg.Tokens,
		queries:       cfg.Queries,
		logger:        logger,
		baseURL:       base,
		httpDo:        httpDo,
		notebookCache: ttlcache.New[listKey, []Notebook](listTTL, nil, ttlcache.StaleOnError()),
		sectionCache:  ttlcache.New[listKey, []Section](listTTL, nil, ttlcache.StaleOnError()),
		pageListCache: ttlcache.New[listKey, []Page](listTTL, nil, ttlcache.StaleOnError()),
		pageCache:     ttlcache.New[pageKey, *PageContent](contentTTL, nil),
	}
}

// --- reads ---------------------------------------------------------------

// ListNotebooks returns the user's notebooks, alphabetical by name.
func (s *Service) ListNotebooks(ctx context.Context, userID int64) ([]Notebook, error) {
	return s.notebookCache.Get(ctx, listKey{userID, "notebooks", ""}, func(ctx context.Context) ([]Notebook, error) {
		body, err := s.do(ctx, userID, http.MethodGet, "/me/onenote/notebooks?$orderby=displayName", "", nil)
		if err != nil {
			return nil, err
		}
		var list graphList[graphNotebook]
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("onenote: decode notebooks: %w", err)
		}
		out := make([]Notebook, 0, len(list.Value))
		for _, g := range list.Value {
			out = append(out, g.toDomain())
		}
		return out, nil
	})
}

// ListSections returns the sections in a notebook, or all sections when
// notebookID is "".
func (s *Service) ListSections(ctx context.Context, userID int64, notebookID string) ([]Section, error) {
	return s.sectionCache.Get(ctx, listKey{userID, "sections", notebookID}, func(ctx context.Context) ([]Section, error) {
		path := "/me/onenote/sections?$orderby=displayName"
		if notebookID != "" {
			path = "/me/onenote/notebooks/" + escapeID(notebookID) + "/sections?$orderby=displayName"
		}
		body, err := s.do(ctx, userID, http.MethodGet, path, "", nil)
		if err != nil {
			return nil, err
		}
		var list graphList[graphSection]
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("onenote: decode sections: %w", err)
		}
		out := make([]Section, 0, len(list.Value))
		for _, g := range list.Value {
			out = append(out, g.toDomain())
		}
		return out, nil
	})
}

// ListPages returns the pages in a section (newest first), or the user's most
// recently-modified pages across all notebooks when sectionID is "".
func (s *Service) ListPages(ctx context.Context, userID int64, sectionID string) ([]Page, error) {
	return s.pageListCache.Get(ctx, listKey{userID, "pages", sectionID}, func(ctx context.Context) ([]Page, error) {
		path := "/me/onenote/pages?$orderby=lastModifiedDateTime%20desc&$top=" + strconv.Itoa(recentPageLimit)
		if sectionID != "" {
			path = "/me/onenote/sections/" + escapeID(sectionID) + "/pages?$orderby=lastModifiedDateTime%20desc&$top=100"
		}
		body, err := s.do(ctx, userID, http.MethodGet, path, "", nil)
		if err != nil {
			return nil, err
		}
		var list graphList[graphPage]
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("onenote: decode pages: %w", err)
		}
		out := make([]Page, 0, len(list.Value))
		for _, g := range list.Value {
			out = append(out, g.toDomain())
		}
		return out, nil
	})
}

// GetPage fetches a page's metadata + parsed content (with editable-element
// ids). Records the page in the user's recent list on every call. The
// returned value is a copy; callers must not assume the cache shares it.
func (s *Service) GetPage(ctx context.Context, userID int64, pageID string) (PageContent, error) {
	pc, err := s.pageCache.Get(ctx, pageKey{userID, pageID}, func(ctx context.Context) (*PageContent, error) {
		return s.fetchPage(ctx, userID, pageID)
	})
	if err != nil {
		return PageContent{}, err
	}
	s.recordRecent(ctx, userID, pc.Page)
	return *pc, nil
}

// fetchPage hits Graph for the page metadata + includeIDs content, parses it,
// and write-throughs the Postgres content cache.
func (s *Service) fetchPage(ctx context.Context, userID int64, pageID string) (*PageContent, error) {
	metaBody, err := s.do(ctx, userID, http.MethodGet, "/me/onenote/pages/"+escapeID(pageID), "", nil)
	if err != nil {
		return nil, err
	}
	var gp graphPage
	if err := json.Unmarshal(metaBody, &gp); err != nil {
		return nil, fmt.Errorf("onenote: decode page: %w", err)
	}

	contentBody, err := s.do(ctx, userID, http.MethodGet, "/me/onenote/pages/"+escapeID(pageID)+"/content?includeIDs=true", "", nil)
	if err != nil {
		return nil, err
	}
	htmlStr := string(contentBody)
	blocks, elements := parsePageHTML(htmlStr)

	pc := &PageContent{
		Page:     gp.toDomain(),
		Blocks:   blocks,
		Elements: elements,
		HTML:     htmlStr,
	}
	for _, b := range blocks {
		if b.Kind.nonText() {
			pc.HasNonText = true
			break
		}
	}
	s.persistPageCache(ctx, userID, pc)
	return pc, nil
}

// --- writes --------------------------------------------------------------

// CreatePage creates a new page in a section from Markdown and returns its
// metadata. Busts the section + recent page-list caches.
func (s *Service) CreatePage(ctx context.Context, userID int64, sectionID string, in NewPage) (Page, error) {
	doc := markdownToPageHTML(in.Title, in.Markdown)
	body, err := s.do(ctx, userID, http.MethodPost,
		"/me/onenote/sections/"+escapeID(sectionID)+"/pages", "text/html", []byte(doc))
	if err != nil {
		return Page{}, err
	}
	var gp graphPage
	if err := json.Unmarshal(body, &gp); err != nil {
		return Page{}, fmt.Errorf("onenote: decode created page: %w", err)
	}
	s.invalidatePageList(userID, sectionID)
	return gp.toDomain(), nil
}

// AppendBlock appends a Markdown block to the end of a page body.
func (s *Service) AppendBlock(ctx context.Context, userID int64, pageID, md string) error {
	cmds := []patchCommand{{Target: "body", Action: "append", Content: markdownToHTML(md)}}
	if err := s.patch(ctx, userID, pageID, cmds); err != nil {
		return err
	}
	s.invalidatePage(userID, pageID)
	return nil
}

// ReplaceElement replaces a single page element (targeted by the id captured
// in GetPage's Elements) with new Markdown content.
func (s *Service) ReplaceElement(ctx context.Context, userID int64, pageID, elementID, md string) error {
	cmds := []patchCommand{{Target: "#" + elementID, Action: "replace", Content: markdownToHTML(md)}}
	if err := s.patch(ctx, userID, pageID, cmds); err != nil {
		return err
	}
	s.invalidatePage(userID, pageID)
	return nil
}

// ReplaceBody overwrites the whole page body with new Markdown. Strategy:
//   - text-only page where every block carries an id and the new content has
//     at least as many blocks: replace each element in place + append extras.
//     This preserves the page id/URL/timestamps.
//   - otherwise (block count shrank, missing ids, or non-text content present):
//     delete + recreate, which yields a NEW page id/URL. The returned Page
//     reflects whichever page now holds the content; callers should re-point
//     any stored reference at it.
//
// When the page has non-text content (images/tables/attachments) a rewrite
// would drop it, so confirmReplace must be true or ErrConfirmRequired is
// returned.
func (s *Service) ReplaceBody(ctx context.Context, userID int64, pageID, md string, confirmReplace bool) (Page, error) {
	pc, err := s.GetPage(ctx, userID, pageID)
	if err != nil {
		return Page{}, err
	}
	if pc.HasNonText && !confirmReplace {
		return Page{}, ErrConfirmRequired
	}

	newBlocks := markdownBlocks(md)

	// Strategy A: in-place replacement preserves page identity. Only safe when
	// the page is purely text and every block has a targetable id.
	if !pc.HasNonText && len(pc.Elements) == len(pc.Blocks) && len(pc.Elements) > 0 && len(newBlocks) >= len(pc.Elements) {
		cmds := make([]patchCommand, 0, len(newBlocks))
		for i, el := range pc.Elements {
			cmds = append(cmds, patchCommand{Target: "#" + el.ID, Action: "replace", Content: newBlocks[i]})
		}
		for _, extra := range newBlocks[len(pc.Elements):] {
			cmds = append(cmds, patchCommand{Target: "body", Action: "append", Content: extra})
		}
		if err := s.patch(ctx, userID, pageID, cmds); err != nil {
			return Page{}, err
		}
		s.invalidatePage(userID, pageID)
		return pc.Page, nil
	}

	// Strategy B: delete + recreate. Non-atomic and changes the page id; the
	// old recent/cache rows are cleared by DeletePage and the new page is
	// recorded on its next GetPage.
	if err := s.DeletePage(ctx, userID, pc.SectionID, pageID); err != nil {
		return Page{}, err
	}
	return s.CreatePage(ctx, userID, pc.SectionID, NewPage{Title: pc.Title, Markdown: md})
}

// DeletePage deletes a page. sectionID is used to bust the section's page-list
// cache; pass "" if unknown (only the recent list is then busted). A 404 from
// Graph is treated as success (already gone).
func (s *Service) DeletePage(ctx context.Context, userID int64, sectionID, pageID string) error {
	_, err := s.do(ctx, userID, http.MethodDelete, "/me/onenote/pages/"+escapeID(pageID), "", nil)
	if err != nil && !isNotFound(err) {
		return err
	}
	s.invalidatePage(userID, pageID)
	s.invalidatePageList(userID, sectionID)
	if s.queries != nil {
		if derr := s.queries.DeleteOneNoteRecentPage(ctx, gen.DeleteOneNoteRecentPageParams{UserID: userID, PageID: pageID}); derr != nil {
			s.logger.Warn("onenote: delete recent row", "page", pageID, "err", derr)
		}
		if derr := s.queries.DeleteOneNotePageCache(ctx, gen.DeleteOneNotePageCacheParams{UserID: userID, PageID: pageID}); derr != nil {
			s.logger.Warn("onenote: delete page cache", "page", pageID, "err", derr)
		}
	}
	return nil
}

// patch issues a content PATCH with the given command list.
func (s *Service) patch(ctx context.Context, userID int64, pageID string, cmds []patchCommand) error {
	body, err := json.Marshal(cmds)
	if err != nil {
		return fmt.Errorf("onenote: marshal patch: %w", err)
	}
	_, err = s.do(ctx, userID, http.MethodPatch,
		"/me/onenote/pages/"+escapeID(pageID)+"/content", "application/json", body)
	return err
}

// patchCommand is one entry in the PATCH /content command array. Position is
// only meaningful for action "insert".
type patchCommand struct {
	Target   string `json:"target"`
	Action   string `json:"action"`
	Position string `json:"position,omitempty"`
	Content  string `json:"content"`
}

// --- cache invalidation --------------------------------------------------

func (s *Service) invalidatePage(userID int64, pageID string) {
	s.pageCache.Invalidate(pageKey{userID, pageID})
}

func (s *Service) invalidatePageList(userID int64, sectionID string) {
	s.pageListCache.Invalidate(listKey{userID, "pages", sectionID})
	s.pageListCache.Invalidate(listKey{userID, "pages", ""}) // recent/all list
}

// --- Postgres thin cache + prefs ----------------------------------------

// RecentPage is a row from the user's recently-viewed list.
type RecentPage struct {
	PageID     string
	SectionID  string
	Title      string
	WebURL     string
	ModifiedAt time.Time
	ViewedAt   time.Time
}

// ListRecentViewed returns the user's recently-opened pages, newest first.
func (s *Service) ListRecentViewed(ctx context.Context, userID int64) ([]RecentPage, error) {
	if s.queries == nil {
		return nil, nil
	}
	rows, err := s.queries.ListOneNoteRecentPages(ctx, gen.ListOneNoteRecentPagesParams{UserID: userID, Limit: int32(recentKeepRows)})
	if err != nil {
		return nil, fmt.Errorf("onenote: list recent: %w", err)
	}
	out := make([]RecentPage, 0, len(rows))
	for _, r := range rows {
		rp := RecentPage{PageID: r.PageID, Title: r.Title}
		if r.SectionID != nil {
			rp.SectionID = *r.SectionID
		}
		if r.WebUrl != nil {
			rp.WebURL = *r.WebUrl
		}
		if r.PageModifiedAt.Valid {
			rp.ModifiedAt = r.PageModifiedAt.Time
		}
		if r.LastViewedAt.Valid {
			rp.ViewedAt = r.LastViewedAt.Time
		}
		out = append(out, rp)
	}
	return out, nil
}

// Prefs holds the user's OneNote defaults.
type Prefs struct {
	DefaultNotebookID string
	DefaultSectionID  string
	LastSyncedAt      time.Time
}

// GetPrefs returns the user's stored prefs (zero value when none set).
func (s *Service) GetPrefs(ctx context.Context, userID int64) (Prefs, error) {
	if s.queries == nil {
		return Prefs{}, nil
	}
	row, err := s.queries.GetOneNotePrefs(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Prefs{}, nil
		}
		return Prefs{}, fmt.Errorf("onenote: get prefs: %w", err)
	}
	p := Prefs{}
	if row.DefaultNotebookID != nil {
		p.DefaultNotebookID = *row.DefaultNotebookID
	}
	if row.DefaultSectionID != nil {
		p.DefaultSectionID = *row.DefaultSectionID
	}
	if row.LastSyncedAt.Valid {
		p.LastSyncedAt = row.LastSyncedAt.Time
	}
	return p, nil
}

// SetDefaults persists the user's default notebook + section.
func (s *Service) SetDefaults(ctx context.Context, userID int64, notebookID, sectionID string) error {
	if s.queries == nil {
		return nil
	}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	return s.queries.UpsertOneNotePrefs(ctx, gen.UpsertOneNotePrefsParams{
		UserID:            userID,
		DefaultNotebookID: strPtr(notebookID),
		DefaultSectionID:  strPtr(sectionID),
		LastSyncedAt:      now,
		CreatedAt:         now,
		UpdatedAt:         now,
	})
}

// recordRecent best-effort upserts the page into the recent list + prunes.
func (s *Service) recordRecent(ctx context.Context, userID int64, page Page) {
	if s.queries == nil {
		return
	}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	err := s.queries.UpsertOneNoteRecentPage(ctx, gen.UpsertOneNoteRecentPageParams{
		UserID:         userID,
		PageID:         page.ID,
		SectionID:      strPtr(page.SectionID),
		Title:          page.Title,
		WebUrl:         strPtr(page.WebURL),
		PageModifiedAt: timestamptz(page.ModifiedAt),
		LastViewedAt:   now,
		CreatedAt:      now,
	})
	if err != nil {
		s.logger.Warn("onenote: record recent", "page", page.ID, "err", err)
		return
	}
	if err := s.queries.PruneOneNoteRecentPages(ctx, gen.PruneOneNoteRecentPagesParams{UserID: userID, Limit: int32(recentKeepRows)}); err != nil {
		s.logger.Warn("onenote: prune recent", "err", err)
	}
}

// persistPageCache best-effort write-throughs a fetched page's content.
func (s *Service) persistPageCache(ctx context.Context, userID int64, pc *PageContent) {
	if s.queries == nil {
		return
	}
	blob, err := json.Marshal(cachedPage{Blocks: pc.Blocks, Elements: pc.Elements, HasNonText: pc.HasNonText})
	if err != nil {
		return
	}
	if err := s.queries.UpsertOneNotePageCache(ctx, gen.UpsertOneNotePageCacheParams{
		UserID:         userID,
		PageID:         pc.ID,
		Html:           pc.HTML,
		Blocks:         blob,
		PageModifiedAt: timestamptz(pc.ModifiedAt),
		FetchedAt:      pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}); err != nil {
		s.logger.Warn("onenote: persist page cache", "page", pc.ID, "err", err)
	}
}

// --- small helpers -------------------------------------------------------

// isNotFound reports whether err is a Graph 404.
func isNotFound(err error) bool {
	var ge *GraphError
	return errors.As(err, &ge) && ge.StatusCode == http.StatusNotFound
}

// strPtr returns nil for the empty string so an absent value stays NULL.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}
