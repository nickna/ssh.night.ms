package screens

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/browser"
	"github.com/nickna/ssh.night.ms/internal/imaging/graphics"
	"github.com/nickna/ssh.night.ms/internal/providers/bookmarks"
	"github.com/nickna/ssh.night.ms/internal/providers/search"
	"github.com/nickna/ssh.night.ms/internal/providers/ttlcache"
	"github.com/nickna/ssh.night.ms/internal/reader"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// browserImageRenderCols caps inline image width so a portrait phone photo
// doesn't blow out the column count on a narrow viewport. Images scale to
// preserve aspect ratio; halfblock packs two pixel rows per cell.
const browserImageRenderCols = 72

// browserFetchTimeout is the per-navigation budget. Reader.Extract gets a
// slightly shorter inner timeout so the outer context can still cancel the
// HTTP read if the readability library hangs.
const browserFetchTimeout = 20 * time.Second
const browserReaderTimeout = 15 * time.Second

type browserMode int

const (
	modeBrowserBrowse browserMode = iota
	modeBrowserURLBar
	modeBrowserBookmarks
)

// browserToastTTL is how long the bookmark-add / error toast stays pinned
// in the URL row. Matches the wall-banner cadence so the screen feels
// consistent with the rest of the app.
const browserToastTTL = 3 * time.Second

// Browser is the standalone reader-mode browser screen. Hotkey 'g' or ':'
// from idle focuses the URL bar; Enter loads the URL (or runs a search when
// the input doesn't parse as one); back/forward walk an in-memory history.
type Browser struct {
	sess *session.Session

	mode  browserMode
	input textinput.Model

	history *browser.History
	cache   *browser.Cache

	article *reader.Article
	scroll  int

	loading    bool
	loadingURL string
	err        string

	// loadCancel tears down the in-flight Extract when the user navigates
	// elsewhere or hits Esc during a load.
	loadCancel context.CancelFunc

	// Image rendering plumbing. The TTL cache holds rendered ANSI rows
	// keyed by URL — a nil value means "fetch already attempted and
	// failed" so we don't retry every frame. The shared sess.Images Pool
	// caps concurrency and singleflight-coalesces; pendingURLs only
	// exists so renderImageBlock can paint "[loading]" while a fetch is
	// in flight (the cache map alone can't distinguish "in flight" from
	// "never requested").
	imageCache  *ttlcache.Cache[string, []string]
	pendingMu   sync.Mutex
	pendingURLs map[string]struct{}

	// Bookmark mode + toast.
	bookmarks       []bookmarks.Bookmark
	bookmarksCursor int
	bookmarksErr    string
	toast           string
	toastExpires    time.Time
}

// NewBrowser is the screen constructor wired into the root model.
func NewBrowser(sess *session.Session) tea.Model {
	in := textinput.New()
	in.Placeholder = "https://example.com — or type to search"
	in.CharLimit = 2048
	return &Browser{
		sess:        sess,
		history:     browser.New(),
		cache:       browser.NewCache(),
		input:       in,
		imageCache:  ttlcache.New[string, []string](0, nil),
		pendingURLs: make(map[string]struct{}),
	}
}

// browserLoadedMsg lands when reader.Extract finishes. URL is echoed so a
// stale response from a cancelled navigation can be ignored.
type browserLoadedMsg struct {
	url     string
	article *reader.Article
	err     error
}

// browserImageFetchedMsg lands when one inline image has been downloaded
// and rendered. Lines is nil on failure (so View can show the alt text).
type browserImageFetchedMsg struct {
	URL   string
	Lines []string
}

// browserSearchResultsMsg carries the result of a DDG fetch back into Update.
// On success, results are folded into a synthetic Article so the article
// renderer can paint them with no special case.
type browserSearchResultsMsg struct {
	query   string
	results []search.Result
	err     error
}

// browserBookmarkAddedMsg / browserBookmarkListMsg / browserBookmarkDeletedMsg
// are the bookmark-service round-trips. err non-nil triggers the toast.
type browserBookmarkAddedMsg struct {
	bookmark bookmarks.Bookmark
	err      error
}

type browserBookmarkListMsg struct {
	items []bookmarks.Bookmark
	err   error
}

type browserBookmarkDeletedMsg struct {
	id  int64
	err error
}

// browserToastExpiredMsg clears the toast banner when the TTL elapses.
type browserToastExpiredMsg struct{ at time.Time }

func (m *Browser) Init() tea.Cmd { return textinput.Blink }

func (m *Browser) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case browserLoadedMsg:
		// Ignore stale responses — they belong to a cancelled load.
		if m.loadingURL != "" && msg.url != m.loadingURL {
			return m, nil
		}
		m.loading = false
		m.loadingURL = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		m.article = msg.article
		m.scroll = 0
		if msg.article != nil {
			m.cache.Put(msg.url, msg.article)
			m.history.Push(browser.Entry{URL: msg.url, Title: msg.article.Title})
			return m, m.scheduleImageFetches(msg.article)
		}
		return m, nil

	case browserImageFetchedMsg:
		m.pendingMu.Lock()
		delete(m.pendingURLs, msg.URL)
		m.pendingMu.Unlock()
		return m, nil

	case browserSearchResultsMsg:
		// Stale-response guard: if user navigated since starting the search,
		// drop the result.
		if m.loadingURL != "" && m.loadingURL != searchSentinel(msg.query) {
			return m, nil
		}
		m.loading = false
		m.loadingURL = ""
		if msg.err != nil {
			m.err = "search: " + msg.err.Error()
			return m, nil
		}
		art := synthesizeSearchArticle(msg.query, msg.results)
		m.err = ""
		m.article = &art
		m.scroll = 0
		m.cache.Put(art.URL, &art)
		m.history.Push(browser.Entry{URL: art.URL, Title: art.Title})
		return m, nil

	case browserBookmarkAddedMsg:
		if msg.err != nil {
			return m, m.showToast("! bookmark failed: " + msg.err.Error())
		}
		return m, m.showToast("bookmarked: " + msg.bookmark.Title)

	case browserBookmarkListMsg:
		if msg.err != nil {
			m.bookmarksErr = msg.err.Error()
			return m, nil
		}
		m.bookmarks = msg.items
		if m.bookmarksCursor >= len(m.bookmarks) {
			m.bookmarksCursor = 0
		}
		return m, nil

	case browserBookmarkDeletedMsg:
		if msg.err != nil {
			return m, m.showToast("! delete failed: " + msg.err.Error())
		}
		// Drop the deleted row from the in-memory list so the view refreshes
		// without a server round-trip.
		out := m.bookmarks[:0]
		for _, b := range m.bookmarks {
			if b.ID != msg.id {
				out = append(out, b)
			}
		}
		m.bookmarks = out
		if m.bookmarksCursor >= len(m.bookmarks) && m.bookmarksCursor > 0 {
			m.bookmarksCursor--
		}
		return m, nil

	case browserToastExpiredMsg:
		// Only clear if the expiry matches the most recent toast — newer
		// toasts that landed inside the TTL would have advanced toastExpires.
		if !msg.at.Before(m.toastExpires) {
			m.toast = ""
		}
		return m, nil

	case tea.KeyMsg:
		switch m.mode {
		case modeBrowserURLBar:
			return m.handleURLBarKey(msg)
		case modeBrowserBookmarks:
			return m.handleBookmarksKey(msg)
		}
		return m.handleBrowseKey(msg)
	}
	return m, nil
}

func (m *Browser) handleBrowseKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		m.cancelLoad()
		return m, nav.Navigate(nav.DestLobby)
	case "g", ":":
		m.mode = modeBrowserURLBar
		current := ""
		if entry, ok := m.history.Current(); ok {
			current = entry.URL
		}
		m.input.SetValue(current)
		m.input.CursorEnd()
		m.input.Focus()
		return m, textinput.Blink
	case "b":
		if entry, ok := m.history.Back(); ok {
			return m, m.loadFromHistory(entry.URL)
		}
	case "f":
		if entry, ok := m.history.Forward(); ok {
			return m, m.loadFromHistory(entry.URL)
		}
	case "r":
		if entry, ok := m.history.Current(); ok {
			m.cache.Forget(entry.URL)
			return m, m.navigate(entry.URL, false)
		}
	case "m":
		return m, m.addBookmark()
	case "M":
		m.mode = modeBrowserBookmarks
		m.bookmarksErr = ""
		m.bookmarksCursor = 0
		return m, m.loadBookmarks()
	case "up", "k":
		if m.scroll > 0 {
			m.scroll--
		}
	case "down", "j":
		m.scroll++ // clamp in View() against rendered line count
	case "pgup":
		m.scroll -= 10
		if m.scroll < 0 {
			m.scroll = 0
		}
	case "pgdown":
		m.scroll += 10
	case "home":
		m.scroll = 0
	}
	return m, nil
}

func (m *Browser) handleURLBarKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeBrowserBrowse
		m.input.Blur()
		return m, nil
	case "enter":
		raw := strings.TrimSpace(m.input.Value())
		if raw == "" {
			m.mode = modeBrowserBrowse
			m.input.Blur()
			return m, nil
		}
		m.mode = modeBrowserBrowse
		m.input.Blur()
		if browser.IsLikelyURL(raw) {
			return m, m.navigate(browser.Normalize(raw), true)
		}
		return m, m.search(browser.StripQueryPrefix(raw))
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

// navigate kicks off a fresh reader.Extract for url. When useCache is true
// (every entry point except an explicit reload), the cache short-circuits to
// an immediate browserLoadedMsg so back/forward feel instant.
func (m *Browser) navigate(rawURL string, useCache bool) tea.Cmd {
	m.cancelLoad()
	m.err = ""
	if useCache {
		if cached, ok := m.cache.Get(rawURL); ok {
			m.loading = false
			m.loadingURL = ""
			return func() tea.Msg {
				return browserLoadedMsg{url: rawURL, article: cached}
			}
		}
	}
	parent := m.sess.Ctx()
	ctx, cancel := context.WithCancel(parent)
	m.loadCancel = cancel
	m.loading = true
	m.loadingURL = rawURL
	return func() tea.Msg {
		ctx2, cancel2 := context.WithTimeout(ctx, browserFetchTimeout)
		defer cancel2()
		article, err := reader.Extract(ctx2, rawURL, browserReaderTimeout)
		if err != nil {
			return browserLoadedMsg{url: rawURL, err: err}
		}
		return browserLoadedMsg{url: rawURL, article: &article}
	}
}

// loadFromHistory navigates without pushing a new history entry. The cache
// usually hits since back/forward only walks previously-loaded URLs, so
// this also avoids re-fetching on every cursor move.
func (m *Browser) loadFromHistory(rawURL string) tea.Cmd {
	if cached, ok := m.cache.Get(rawURL); ok {
		m.err = ""
		m.loading = false
		m.article = cached
		m.scroll = 0
		return m.scheduleImageFetches(cached)
	}
	// Cache miss (entry evicted under pressure) — refetch but don't double-
	// push history. Use a dedicated tag so the loaded handler skips Push.
	return m.navigate(rawURL, false)
}

// search fires a DDG (or whatever Provider) search in a tea.Cmd, then folds
// the result list into a synthetic Article so the article renderer handles
// it with no new UI path. searchSentinel namespaces the loadingURL so the
// stale-response guard in Update can distinguish search-vs-navigate.
func (m *Browser) search(query string) tea.Cmd {
	m.cancelLoad()
	m.err = ""
	if m.sess.Search == nil {
		m.loading = false
		m.err = "search provider not configured"
		return nil
	}
	provider := m.sess.Search
	parent := m.sess.Ctx()
	ctx, cancel := context.WithCancel(parent)
	m.loadCancel = cancel
	m.loading = true
	m.loadingURL = searchSentinel(query)
	return func() tea.Msg {
		ctx2, cancel2 := context.WithTimeout(ctx, browserFetchTimeout)
		defer cancel2()
		results, err := provider.Search(ctx2, query, 20)
		return browserSearchResultsMsg{query: query, results: results, err: err}
	}
}

// addBookmark snapshots the current article and persists it. Failure or
// missing service surfaces as a toast.
func (m *Browser) addBookmark() tea.Cmd {
	if m.sess.Bookmarks == nil {
		return m.showToast("! bookmarks: service not configured")
	}
	if m.sess.Identity.UserID == 0 {
		return m.showToast("! bookmarks: log in first")
	}
	entry, ok := m.history.Current()
	if !ok || m.article == nil {
		return m.showToast("! nothing to bookmark — load a page first")
	}
	url := entry.URL
	title := m.article.Title
	if title == "" {
		title = url
	}
	userID := m.sess.Identity.UserID
	bookmarksSvc := m.sess.Bookmarks
	parent := m.sess.Ctx()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		bm, err := bookmarksSvc.Add(ctx, userID, url, title)
		return browserBookmarkAddedMsg{bookmark: bm, err: err}
	}
}

// loadBookmarks fetches the user's saved URLs for the bookmark list view.
func (m *Browser) loadBookmarks() tea.Cmd {
	if m.sess.Bookmarks == nil || m.sess.Identity.UserID == 0 {
		return func() tea.Msg {
			return browserBookmarkListMsg{err: fmt.Errorf("bookmarks: service or identity missing")}
		}
	}
	userID := m.sess.Identity.UserID
	bookmarksSvc := m.sess.Bookmarks
	parent := m.sess.Ctx()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		items, err := bookmarksSvc.List(ctx, userID)
		return browserBookmarkListMsg{items: items, err: err}
	}
}

// deleteBookmark removes one entry, scoped to the current user.
func (m *Browser) deleteBookmark(id int64) tea.Cmd {
	if m.sess.Bookmarks == nil || m.sess.Identity.UserID == 0 {
		return nil
	}
	userID := m.sess.Identity.UserID
	bookmarksSvc := m.sess.Bookmarks
	parent := m.sess.Ctx()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		err := bookmarksSvc.Delete(ctx, userID, id)
		return browserBookmarkDeletedMsg{id: id, err: err}
	}
}

// showToast pins a message above the URL row for browserToastTTL. Returns
// a tea.Cmd that fires the expiry message; callers tea.Batch it with any
// follow-up work.
func (m *Browser) showToast(message string) tea.Cmd {
	m.toast = message
	m.toastExpires = time.Now().Add(browserToastTTL)
	return tea.Tick(browserToastTTL, func(t time.Time) tea.Msg {
		return browserToastExpiredMsg{at: t}
	})
}

// handleBookmarksKey runs the bookmark-list mode. Enter opens the cursor
// row; d deletes; Esc / M / q returns to browse.
func (m *Browser) handleBookmarksKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q", "M":
		m.mode = modeBrowserBrowse
		return m, nil
	case "up", "k":
		if m.bookmarksCursor > 0 {
			m.bookmarksCursor--
		}
	case "down", "j":
		if m.bookmarksCursor < len(m.bookmarks)-1 {
			m.bookmarksCursor++
		}
	case "home":
		m.bookmarksCursor = 0
	case "end":
		if len(m.bookmarks) > 0 {
			m.bookmarksCursor = len(m.bookmarks) - 1
		}
	case "enter":
		if m.bookmarksCursor < len(m.bookmarks) {
			target := m.bookmarks[m.bookmarksCursor].URL
			m.mode = modeBrowserBrowse
			return m, m.navigate(target, true)
		}
	case "d":
		if m.bookmarksCursor < len(m.bookmarks) {
			id := m.bookmarks[m.bookmarksCursor].ID
			return m, m.deleteBookmark(id)
		}
	}
	return m, nil
}

// searchSentinel namespaces a query so loadingURL doesn't accidentally
// collide with a real http(s) URL when comparing stale-response guards.
func searchSentinel(query string) string { return "search:" + query }

// synthesizeSearchArticle builds a *reader.Article from a search result list
// so the article renderer can paint search results with zero special-casing.
// Each hit becomes Heading + Paragraph (snippet) + Paragraph (url) blocks.
func synthesizeSearchArticle(query string, results []search.Result) reader.Article {
	a := reader.Article{
		Title:  "Search: " + query,
		Byline: fmt.Sprintf("%d results", len(results)),
		Host:   "search",
		URL:    searchSentinel(query),
	}
	if len(results) == 0 {
		a.Blocks = []reader.Block{{Kind: reader.BlockParagraph, Text: "No results returned."}}
		return a
	}
	for i, r := range results {
		title := fmt.Sprintf("%d. %s", i+1, r.Title)
		a.Blocks = append(a.Blocks, reader.Block{Kind: reader.BlockHeading, Text: title})
		if r.Snippet != "" {
			a.Blocks = append(a.Blocks, reader.Block{Kind: reader.BlockParagraph, Text: r.Snippet})
		}
		if r.URL != "" {
			a.Blocks = append(a.Blocks, reader.Block{Kind: reader.BlockParagraph, Text: r.URL})
		}
	}
	return a
}

func (m *Browser) cancelLoad() {
	if m.loadCancel != nil {
		m.loadCancel()
		m.loadCancel = nil
	}
	m.loading = false
	m.loadingURL = ""
}

// scheduleImageFetches walks the article's BlockImage entries and starts a
// download for any URL not already cached or in-flight. Returns the batch
// of tea.Cmds to dispatch.
func (m *Browser) scheduleImageFetches(article *reader.Article) tea.Cmd {
	if article == nil {
		return nil
	}
	var cmds []tea.Cmd
	m.pendingMu.Lock()
	for _, block := range article.Blocks {
		if block.Kind != reader.BlockImage || block.URL == "" {
			continue
		}
		if _, ok := m.imageCache.Peek(block.URL); ok {
			continue
		}
		if _, ok := m.pendingURLs[block.URL]; ok {
			continue
		}
		m.pendingURLs[block.URL] = struct{}{}
		cmds = append(cmds, m.fetchImage(block.URL))
	}
	m.pendingMu.Unlock()
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// fetchImage downloads via the shared image pool, renders to half-block /
// kitty / iTerm based on the session's negotiated graphics protocol, and
// caches the result. Failures are cached as nil-lines so renderImageBlock
// can paint a stable error placeholder without retrying every frame.
func (m *Browser) fetchImage(rawURL string) tea.Cmd {
	parent := m.sess.Ctx()
	cols := browserImageRenderCols
	if m.sess.Width > 0 && m.sess.Width-4 < cols {
		cols = m.sess.Width - 4
		if cols < 20 {
			cols = 20
		}
	}
	gfx := m.sess.Graphics
	return func() tea.Msg {
		lines, _ := m.imageCache.Get(parent, rawURL, func(ctx context.Context) ([]string, error) {
			img, err := m.sess.Images.Fetch(ctx, rawURL)
			if err != nil {
				// Cache failures as nil-lines so subsequent renders show a
				// stable "unavailable" placeholder rather than retrying.
				return nil, nil
			}
			return graphics.EncodeWithFallback(gfx, img, cols), nil
		})
		return browserImageFetchedMsg{URL: rawURL, Lines: lines}
	}
}

var (
	browserTitle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	browserHint        = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	browserURLStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan))
	browserErrStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	browserHostStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	browserBodyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText))
	browserHeadStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	browserBylineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted))
	browserQuoteStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim)).Italic(true)
	browserCodeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan)).Background(lipgloss.Color(theme.ColorSurfaceAlt))
	browserAltStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim)).Italic(true)
	browserToastStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow)).Background(lipgloss.Color(theme.ColorSurfaceAlt)).Padding(0, 1)
	browserActiveRow   = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color(theme.ColorSurfaceAlt))
)

func (m *Browser) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}

	var b strings.Builder
	hasToast := m.toast != "" && time.Now().Before(m.toastExpires)
	if hasToast {
		b.WriteString(browserToastStyle.Render(m.toast))
		b.WriteString("\n")
	}
	b.WriteString(m.renderHeader())
	b.WriteString("\n")

	bodyHeight := m.sess.Height - 3 // header + hint + status bar (root reserves status)
	if m.mode == modeBrowserURLBar {
		bodyHeight-- // URL bar input expands one row when focused
	}
	if hasToast {
		bodyHeight--
	}
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	body := m.renderBody(bodyHeight)
	b.WriteString(body)
	b.WriteString("\n")
	b.WriteString(m.renderHintBar())
	return b.String()
}

func (m *Browser) renderHeader() string {
	if m.mode == modeBrowserURLBar {
		// Expanded: label + textinput + sub-hint.
		head := browserTitle.Render("Browser") + "  " + browserHint.Render("Enter load · Esc cancel")
		return head + "\n" + m.input.View()
	}
	curURL := ""
	if entry, ok := m.history.Current(); ok {
		curURL = entry.URL
	}
	if m.loading && m.loadingURL != "" {
		curURL = m.loadingURL
	}
	pos := ""
	if cur, total := m.history.Position(); total > 0 {
		pos = fmt.Sprintf("%d/%d", cur, total)
	}
	left := browserTitle.Render("Browser")
	right := pos
	mid := ""
	if curURL != "" {
		mid = "  " + browserURLStyle.Render(truncateMiddle(curURL, m.sess.Width-len("Browser")-len(right)-6))
	}
	if right != "" {
		right = "  " + browserHint.Render(right)
	}
	return left + mid + right
}

func (m *Browser) renderBody(height int) string {
	if m.mode == modeBrowserBookmarks {
		return m.renderBookmarks(height)
	}
	switch {
	case m.loading:
		label := safeHost(m.loadingURL)
		if strings.HasPrefix(m.loadingURL, "search:") {
			label = "search"
		}
		return browserHint.Render("fetching " + browserHostStyle.Render(label) + "…")
	case m.err != "":
		var sb strings.Builder
		sb.WriteString(browserErrStyle.Render("! " + m.err))
		sb.WriteString("\n\n")
		sb.WriteString(browserHint.Render("press r to retry · g for new URL · Esc back"))
		return sb.String()
	case m.article == nil:
		return m.renderIdle(height)
	}
	return m.renderArticle(height)
}

func (m *Browser) renderBookmarks(height int) string {
	var b strings.Builder
	b.WriteString(browserHeadStyle.Render("Bookmarks"))
	b.WriteString("  ")
	b.WriteString(browserHint.Render("Enter open · d delete · Esc back"))
	b.WriteString("\n\n")
	if m.bookmarksErr != "" {
		b.WriteString(browserErrStyle.Render("! " + m.bookmarksErr))
		return b.String()
	}
	if len(m.bookmarks) == 0 {
		b.WriteString(browserHint.Render("nothing saved yet — press m on any loaded page to bookmark it"))
		return b.String()
	}
	rows := height - 3
	if rows < 1 {
		rows = 1
	}
	start := 0
	if m.bookmarksCursor >= rows {
		start = m.bookmarksCursor - rows + 1
	}
	end := start + rows
	if end > len(m.bookmarks) {
		end = len(m.bookmarks)
	}
	width := m.sess.Width - 4
	if width < 40 {
		width = 40
	}
	for i := start; i < end; i++ {
		bm := m.bookmarks[i]
		title := truncateMiddle(bm.Title, width-30)
		host := safeHost(bm.URL)
		row := fmt.Sprintf("%s  %s", title, browserHostStyle.Render(host))
		if i == m.bookmarksCursor {
			b.WriteString("▸ " + browserActiveRow.Render(row))
		} else {
			b.WriteString("  " + row)
		}
		b.WriteString("\n")
	}
	if end < len(m.bookmarks) {
		b.WriteString(browserHint.Render(fmt.Sprintf("  … +%d more below", len(m.bookmarks)-end)))
	}
	return b.String()
}

func (m *Browser) renderIdle(_ int) string {
	var b strings.Builder
	b.WriteString(browserHint.Render("Reader-mode browser. Press "))
	b.WriteString(browserURLStyle.Render("g"))
	b.WriteString(browserHint.Render(" to enter a URL, or "))
	b.WriteString(browserURLStyle.Render(":"))
	b.WriteString(browserHint.Render(" if you prefer the vim hotkey."))
	b.WriteString("\n\n")
	b.WriteString(browserHint.Render("URLs that look like a hostname work without a scheme — typing "))
	b.WriteString(browserURLStyle.Render("go.dev"))
	b.WriteString(browserHint.Render(" will load "))
	b.WriteString(browserURLStyle.Render("https://go.dev"))
	b.WriteString(browserHint.Render("."))
	return b.String()
}

func (m *Browser) renderArticle(height int) string {
	width := m.sess.Width - 4
	if width < 40 {
		width = 40
	}
	maxLineW := width
	if maxLineW > 90 {
		maxLineW = 90
	}

	lines := m.buildArticleLines(maxLineW)

	maxScroll := len(lines) - height
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	end := m.scroll + height
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	for _, ln := range lines[m.scroll:end] {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	if end < len(lines) {
		b.WriteString(browserHint.Render(fmt.Sprintf("  … %d more lines below", len(lines)-end)))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Browser) buildArticleLines(maxLineW int) []string {
	var lines []string
	a := m.article
	for _, l := range wrapToWidth(a.Title, maxLineW) {
		lines = append(lines, browserHeadStyle.Render(l))
	}
	meta := ""
	if a.Byline != "" {
		meta = a.Byline
	}
	if a.Host != "" {
		if meta != "" {
			meta += " · "
		}
		meta += a.Host
	}
	if meta != "" {
		for _, l := range wrapToWidth(meta, maxLineW) {
			lines = append(lines, browserBylineStyle.Render(l))
		}
	}
	lines = append(lines, "")

	for _, block := range a.Blocks {
		switch block.Kind {
		case reader.BlockHeading:
			for _, l := range wrapToWidth(block.Text, maxLineW) {
				lines = append(lines, browserHeadStyle.Render(l))
			}
		case reader.BlockQuote:
			for _, l := range wrapToWidth(block.Text, maxLineW-2) {
				lines = append(lines, browserQuoteStyle.Render("│ "+l))
			}
		case reader.BlockCode:
			for _, raw := range strings.Split(block.Text, "\n") {
				if raw == "" {
					lines = append(lines, browserCodeStyle.Render(strings.Repeat(" ", maxLineW)))
					continue
				}
				for _, l := range wrapToWidth(raw, maxLineW-2) {
					padded := "  " + l
					if extra := maxLineW - len([]rune(padded)); extra > 0 {
						padded += strings.Repeat(" ", extra)
					}
					lines = append(lines, browserCodeStyle.Render(padded))
				}
			}
		case reader.BlockList:
			for _, item := range strings.Split(block.Text, "\n") {
				wrapped := wrapToWidth(item, maxLineW)
				if len(wrapped) == 0 {
					continue
				}
				lines = append(lines, browserBodyStyle.Render(wrapped[0]))
				for _, cont := range wrapped[1:] {
					lines = append(lines, browserBodyStyle.Render("  "+cont))
				}
			}
		case reader.BlockImage:
			lines = append(lines, m.renderImageBlock(block)...)
		default:
			for _, l := range wrapToWidth(block.Text, maxLineW) {
				lines = append(lines, browserBodyStyle.Render(l))
			}
		}
		lines = append(lines, "")
	}
	return lines
}

// renderImageBlock returns the lines for one BlockImage. When the image has
// been fetched + rendered, those lines come back directly. Otherwise a
// single-line placeholder is shown until the fetch completes; cached
// failures (nil-lines in the cache) render the "unavailable" placeholder.
func (m *Browser) renderImageBlock(block reader.Block) []string {
	cached, hasCached := m.imageCache.Peek(block.URL)
	m.pendingMu.Lock()
	_, pending := m.pendingURLs[block.URL]
	m.pendingMu.Unlock()

	if hasCached && len(cached) > 0 {
		return cached
	}
	alt := block.Text
	if alt == "" {
		alt = safeHost(block.URL)
	}
	if pending {
		return []string{browserAltStyle.Render("[ loading " + safeHost(block.URL) + " — " + truncateMiddle(alt, 40) + " ]")}
	}
	// Cached failure (nil-lines) or never-scheduled — both render unavailable.
	return []string{browserAltStyle.Render("[ image unavailable: " + truncateMiddle(alt, 60) + " ]")}
}

func (m *Browser) renderHintBar() string {
	var parts []string
	switch m.mode {
	case modeBrowserURLBar:
		parts = []string{
			browserHint.Render("Enter load"),
			browserHint.Render("Esc cancel"),
		}
	case modeBrowserBookmarks:
		parts = []string{
			browserHint.Render("Enter open"),
			browserHint.Render("d delete"),
			browserHint.Render("Esc back"),
		}
	default:
		parts = []string{browserHint.Render("g url")}
		if m.history.CanBack() {
			parts = append(parts, browserHint.Render("b back"))
		}
		if m.history.CanForward() {
			parts = append(parts, browserHint.Render("f fwd"))
		}
		if _, ok := m.history.Current(); ok {
			parts = append(parts, browserHint.Render("r reload"))
			parts = append(parts, browserHint.Render("m bookmark"))
		}
		parts = append(parts, browserHint.Render("M list"))
		parts = append(parts, browserHint.Render("Esc lobby"))
	}
	return strings.Join(parts, "  ·  ")
}

// safeHost returns the host portion of rawURL, or rawURL itself when it
// doesn't parse. Used in status/error lines so we don't spam a full URL.
func safeHost(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Host
}

// truncateMiddle keeps the head and tail of s when it exceeds maxRunes,
// replacing the cut with an ellipsis. Useful for URLs where the meaningful
// path lives at both ends.
func truncateMiddle(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	headLen := maxRunes / 2
	tailLen := maxRunes - headLen - 1
	if tailLen < 0 {
		tailLen = 0
	}
	return string(runes[:headLen]) + "…" + string(runes[len(runes)-tailLen:])
}
