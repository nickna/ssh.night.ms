// Finance screen — multi-asset watchlist (stocks + crypto + FX) over a
// per-user persisted list, with inline sparklines, a finance-news pane, and
// a drill-in Detail mode. Mirrors ssh.night.ms/.../FinanceScreen.cs with the
// Go palette and colored ▲/▼ deltas as visual additions.
package screens

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/providers/finance"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Tunables. Twelve rows fit on-screen without scrolling at 80×24; twenty is
// the per-user hard cap to keep the per-refresh fan-out polite against
// Yahoo/CoinGecko free tiers.
const (
	maxWatchlistRows = 20
	maxNewsItems     = 15
	sparklineWidth   = 12
	dbCallTimeout    = 5 * time.Second
	quoteFanoutTO    = 10 * time.Second
)

// defaultSeed is the curated starter watchlist a brand-new user sees on
// first open. Mirrors the C# FinanceScreen seed.
var defaultSeed = []struct {
	Symbol    string
	Canonical string
	Kind      finance.Kind
}{
	{"AAPL", "AAPL", finance.KindStock},
	{"MSFT", "MSFT", finance.KindStock},
	{"BTC", "bitcoin", finance.KindCrypto},
	{"ETH", "ethereum", finance.KindCrypto},
	{"EUR/USD", "EURUSD", finance.KindFx},
}

// ──────────── modes / focus ────────────

type financeMode int

const (
	fmList financeMode = iota
	fmAddPrompt
	fmEditPrompt
	fmConfirmDelete
	fmDetail
)

type financeFocus int

const (
	ffRows financeFocus = iota
	ffNews
)

type statusKind int

const (
	stInfo statusKind = iota
	stSuccess
	stWarning
)

// ──────────── model ────────────

type financeRow struct {
	item  gen.UserWatchlistItem
	quote *finance.Quote
	spark []float64
}

type Finance struct {
	sess *session.Session

	mode  financeMode
	focus financeFocus

	rows      []financeRow
	rowCursor int
	loading   bool
	loadErr   string

	news       []finance.Headline
	newsCursor int

	// Prompt input shared by Add + Edit modes.
	input      textinput.Model
	editingID  int64
	promptHint string
	promptErr  string

	// Two-step delete confirmation: holds the row id we're about to delete.
	deletePending int64

	// Detail-mode payload.
	detail        *finance.Detail
	detailKind    finance.Kind
	detailSym     string
	detailNews    []finance.Headline
	detailLoading bool
	detailErr     string

	// Status line content; cleared on next user action.
	statusMsg  string
	statusKind statusKind
}

func NewFinance(sess *session.Session) tea.Model {
	in := textinput.New()
	in.Placeholder = "AAPL · BTC · EUR/USD · s:NVDA · c:doge · fx:gbpjpy"
	in.CharLimit = 32
	return &Finance{
		sess:    sess,
		loading: true,
		input:   in,
	}
}

func (m *Finance) Init() tea.Cmd { return m.loadAll() }

// ──────────── async loads ────────────

type watchlistLoadedMsg struct {
	items []gen.UserWatchlistItem
	err   error
}

type rowDataMsg struct {
	id    int64
	quote *finance.Quote
	spark []float64
}

type financeNewsLoadedMsg struct {
	items []finance.Headline
	err   error
}

type itemMutatedMsg struct {
	op       string // "added" | "updated" | "deleted" | "reordered"
	symbol   string
	err      error
	selectAt int // post-reload cursor target
}

type detailLoadedMsg struct {
	detail *finance.Detail
	news   []finance.Headline
	err    error
}

// loadAll: list (seed if empty) → per-row quote+spark fan-out → finance news.
func (m *Finance) loadAll() tea.Cmd {
	return tea.Batch(m.loadWatchlist(), m.loadNews())
}

func (m *Finance) ctx() (context.Context, context.CancelFunc) {
	parent := m.sess.Ctx()
	return context.WithTimeout(parent, dbCallTimeout)
}

func (m *Finance) loadWatchlist() tea.Cmd {
	q := m.sess.Queries
	userID := m.sess.Identity.UserID
	if q == nil || userID == 0 {
		return func() tea.Msg {
			return watchlistLoadedMsg{err: fmt.Errorf("watchlist requires a logged-in user")}
		}
	}
	return func() tea.Msg {
		ctx, cancel := m.ctx()
		defer cancel()
		items, err := q.ListWatchlistItems(ctx, userID)
		if err != nil {
			return watchlistLoadedMsg{err: err}
		}
		if len(items) == 0 {
			items, err = seedAndList(ctx, q, userID)
			if err != nil {
				return watchlistLoadedMsg{err: err}
			}
		}
		return watchlistLoadedMsg{items: items}
	}
}

// seedAndList inserts the default watchlist for a new user. If a concurrent
// session seeded ahead of us (unique index violation), swallow and re-list
// what's there now.
func seedAndList(ctx context.Context, q *gen.Queries, userID int64) ([]gen.UserWatchlistItem, error) {
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	for i, s := range defaultSeed {
		_, err := q.AddWatchlistItem(ctx, gen.AddWatchlistItemParams{
			UserID:    userID,
			Symbol:    s.Symbol,
			Canonical: s.Canonical,
			Kind:      int32(s.Kind),
			SortOrder: int32(i),
			CreatedAt: now,
		})
		if err != nil && !isDuplicateKey(err) {
			return nil, err
		}
	}
	return q.ListWatchlistItems(ctx, userID)
}

func isDuplicateKey(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

func (m *Finance) loadRows(items []gen.UserWatchlistItem) tea.Cmd {
	provider := m.sess.Finance
	if provider == nil {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(items))
	for _, it := range items {
		id := it.ID
		kind := finance.Kind(it.Kind)
		canon := it.Canonical
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := m.sess.CtxWithTimeout(quoteFanoutTO)
			defer cancel()
			var (
				quote *finance.Quote
				spark []float64
				wg    sync.WaitGroup
			)
			wg.Add(2)
			go func() {
				defer wg.Done()
				quote, _ = provider.GetQuote(ctx, kind, canon)
			}()
			go func() {
				defer wg.Done()
				spark, _ = provider.GetSparkline(ctx, kind, canon)
			}()
			wg.Wait()
			return rowDataMsg{id: id, quote: quote, spark: spark}
		})
	}
	return tea.Batch(cmds...)
}

func (m *Finance) loadNews() tea.Cmd {
	provider := m.sess.FinanceNews
	if provider == nil {
		return nil
	}
	tickers := m.stockTickers()
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(quoteFanoutTO)
		defer cancel()
		items, err := provider.ForTickers(ctx, tickers, maxNewsItems)
		return financeNewsLoadedMsg{items: items, err: err}
	}
}

func (m *Finance) stockTickers() []string {
	out := make([]string, 0)
	for _, r := range m.rows {
		if finance.Kind(r.item.Kind) == finance.KindStock {
			out = append(out, r.item.Canonical)
		}
	}
	return out
}

// ──────────── mutations ────────────

func (m *Finance) addItem(symbol string, kind finance.Kind, canonical string) tea.Cmd {
	q := m.sess.Queries
	userID := m.sess.Identity.UserID
	rowCount := int32(len(m.rows))
	if int(rowCount) >= maxWatchlistRows {
		m.setStatus(fmt.Sprintf("[!] watchlist is full (%d). Delete a symbol first.", maxWatchlistRows), stWarning)
		return nil
	}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	return func() tea.Msg {
		ctx, cancel := m.ctx()
		defer cancel()
		_, err := q.AddWatchlistItem(ctx, gen.AddWatchlistItemParams{
			UserID:    userID,
			Symbol:    symbol,
			Canonical: canonical,
			Kind:      int32(kind),
			SortOrder: rowCount,
			CreatedAt: now,
		})
		if isDuplicateKey(err) {
			return itemMutatedMsg{op: "added", symbol: symbol,
				err: fmt.Errorf("'%s' is already on your watchlist", symbol)}
		}
		return itemMutatedMsg{op: "added", symbol: symbol, err: err, selectAt: int(rowCount)}
	}
}

func (m *Finance) updateItem(id int64, symbol string, kind finance.Kind, canonical string) tea.Cmd {
	q := m.sess.Queries
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.ctx()
		defer cancel()
		_, err := q.UpdateWatchlistItem(ctx, gen.UpdateWatchlistItemParams{
			ID:        id,
			UserID:    userID,
			Symbol:    symbol,
			Canonical: canonical,
			Kind:      int32(kind),
		})
		if isDuplicateKey(err) {
			return itemMutatedMsg{op: "updated", symbol: symbol,
				err: fmt.Errorf("another row already uses '%s'", canonical)}
		}
		return itemMutatedMsg{op: "updated", symbol: symbol, err: err}
	}
}

func (m *Finance) deleteItem(id int64, symbol string) tea.Cmd {
	q := m.sess.Queries
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.ctx()
		defer cancel()
		err := q.DeleteWatchlistItem(ctx, gen.DeleteWatchlistItemParams{
			ID: id, UserID: userID,
		})
		return itemMutatedMsg{op: "deleted", symbol: symbol, err: err}
	}
}

// reorder swaps sort_order between the cursor row and its neighbor. Returns
// the new cursor target so the row stays "under" the cursor after reload.
func (m *Finance) reorder(delta int) tea.Cmd {
	if m.rowCursor < 0 || m.rowCursor >= len(m.rows) {
		return nil
	}
	target := m.rowCursor + delta
	if target < 0 || target >= len(m.rows) {
		return nil
	}
	a := m.rows[m.rowCursor].item
	b := m.rows[target].item
	q := m.sess.Queries
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.ctx()
		defer cancel()
		// Two sequential updates — the (user_id, sort_order) tuple isn't
		// constrained as unique so a transient dupe between the two writes
		// is harmless. Failures leave the rows untouched.
		if err := q.SetWatchlistSortOrder(ctx, gen.SetWatchlistSortOrderParams{
			ID: a.ID, UserID: userID, SortOrder: b.SortOrder,
		}); err != nil {
			return itemMutatedMsg{op: "reordered", err: err}
		}
		if err := q.SetWatchlistSortOrder(ctx, gen.SetWatchlistSortOrderParams{
			ID: b.ID, UserID: userID, SortOrder: a.SortOrder,
		}); err != nil {
			return itemMutatedMsg{op: "reordered", err: err}
		}
		return itemMutatedMsg{op: "reordered", selectAt: target}
	}
}

// ──────────── detail mode ────────────

func (m *Finance) openDetail() tea.Cmd {
	if m.rowCursor < 0 || m.rowCursor >= len(m.rows) {
		return nil
	}
	row := m.rows[m.rowCursor]
	m.mode = fmDetail
	m.detailKind = finance.Kind(row.item.Kind)
	m.detailSym = row.item.Symbol
	m.detail = nil
	m.detailNews = nil
	m.detailLoading = true
	m.detailErr = ""
	return m.loadDetail(row.item)
}

func (m *Finance) loadDetail(item gen.UserWatchlistItem) tea.Cmd {
	provider := m.sess.Finance
	news := m.sess.FinanceNews
	kind := finance.Kind(item.Kind)
	canon := item.Canonical
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(quoteFanoutTO)
		defer cancel()
		var (
			d        *finance.Detail
			derr     error
			hl       []finance.Headline
			tickers  []string
			wg       sync.WaitGroup
		)
		if kind == finance.KindStock {
			tickers = []string{canon}
		}
		wg.Add(2)
		go func() {
			defer wg.Done()
			d, derr = provider.GetDetail(ctx, kind, canon)
		}()
		go func() {
			defer wg.Done()
			if news != nil {
				hl, _ = news.ForTickers(ctx, tickers, 10)
			}
		}()
		wg.Wait()
		return detailLoadedMsg{detail: d, news: hl, err: derr}
	}
}

// ──────────── Update ────────────

func (m *Finance) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case watchlistLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.loadErr = msg.err.Error()
			return m, nil
		}
		m.loadErr = ""
		m.rows = make([]financeRow, len(msg.items))
		for i, it := range msg.items {
			m.rows[i].item = it
		}
		if m.rowCursor >= len(m.rows) {
			m.rowCursor = max0(len(m.rows) - 1)
		}
		return m, tea.Batch(m.loadRows(msg.items), m.loadNews())

	case rowDataMsg:
		for i := range m.rows {
			if m.rows[i].item.ID == msg.id {
				m.rows[i].quote = msg.quote
				m.rows[i].spark = msg.spark
				break
			}
		}
		return m, nil

	case financeNewsLoadedMsg:
		if msg.err != nil {
			m.news = nil
			return m, nil
		}
		m.news = msg.items
		if m.newsCursor >= len(m.news) {
			m.newsCursor = max0(len(m.news) - 1)
		}
		return m, nil

	case itemMutatedMsg:
		if msg.err != nil {
			m.setStatus("[!] "+msg.op+" failed: "+msg.err.Error(), stWarning)
			return m, nil
		}
		switch msg.op {
		case "added":
			m.setStatus("added "+msg.symbol, stSuccess)
		case "updated":
			m.setStatus("updated "+msg.symbol, stSuccess)
		case "deleted":
			m.setStatus("deleted "+msg.symbol, stSuccess)
		}
		// Re-list so the new ordering / membership lands.
		m.mode = fmList
		m.deletePending = 0
		if msg.selectAt > 0 {
			m.rowCursor = msg.selectAt
		}
		return m, m.loadWatchlist()

	case detailLoadedMsg:
		m.detailLoading = false
		if msg.err != nil {
			m.detailErr = msg.err.Error()
		}
		m.detail = msg.detail
		m.detailNews = msg.news
		return m, nil

	case nav.NavigateMsg:
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Finance) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case fmAddPrompt, fmEditPrompt:
		return m.handlePromptKey(k)
	case fmConfirmDelete:
		return m.handleConfirmKey(k)
	case fmDetail:
		return m.handleDetailKey(k)
	default:
		return m.handleListKey(k)
	}
}

func (m *Finance) handleListKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Cancel any pending two-step delete on the next keypress.
	if m.deletePending != 0 && k.String() != "d" {
		m.deletePending = 0
	}

	switch k.String() {
	case "esc":
		return m, nav.Navigate(nav.DestLobby)
	case "r":
		if !m.loading {
			m.loading = true
			m.loadErr = ""
			m.clearStatus()
			return m, m.loadAll()
		}
	case "n":
		if len(m.news) > 0 {
			m.focus = ffNews
		}
		return m, nil
	case "a":
		m.beginAddPrompt()
		return m, nil
	case "e":
		m.beginEditPrompt()
		return m, nil
	case "d":
		return m.handleDelete()
	case "k":
		if m.focus == ffRows {
			return m, m.reorder(-1)
		}
	case "j":
		if m.focus == ffRows {
			return m, m.reorder(+1)
		}
	case "enter":
		if m.focus == ffRows {
			return m, m.openDetail()
		}
		// In news focus, Enter would open a reader — punted to v1.1.
	case "up":
		m.moveCursor(-1)
		return m, nil
	case "down":
		m.moveCursor(+1)
		return m, nil
	}
	return m, nil
}

func (m *Finance) moveCursor(delta int) {
	if m.focus == ffNews {
		m.newsCursor = clamp(m.newsCursor+delta, 0, max0(len(m.news)-1))
		if m.newsCursor == 0 && delta < 0 {
			m.focus = ffRows
		}
		return
	}
	if len(m.rows) == 0 {
		return
	}
	m.rowCursor = clamp(m.rowCursor+delta, 0, len(m.rows)-1)
}

func (m *Finance) handleDelete() (tea.Model, tea.Cmd) {
	if m.focus != ffRows || m.rowCursor < 0 || m.rowCursor >= len(m.rows) {
		return m, nil
	}
	row := m.rows[m.rowCursor]
	if m.deletePending == row.item.ID {
		// Second press → commit.
		m.deletePending = 0
		return m, m.deleteItem(row.item.ID, row.item.Symbol)
	}
	m.deletePending = row.item.ID
	m.setStatus(fmt.Sprintf("press D again to delete %s", row.item.Symbol), stWarning)
	return m, nil
}

func (m *Finance) beginAddPrompt() {
	m.mode = fmAddPrompt
	m.editingID = 0
	m.promptHint = "add symbol — Enter to save · Esc cancel"
	m.promptErr = ""
	m.input.SetValue("")
	m.input.Focus()
}

func (m *Finance) beginEditPrompt() {
	if m.rowCursor < 0 || m.rowCursor >= len(m.rows) {
		return
	}
	row := m.rows[m.rowCursor]
	m.mode = fmEditPrompt
	m.editingID = row.item.ID
	m.promptHint = "edit " + row.item.Symbol + " — Enter to save · Esc cancel"
	m.promptErr = ""
	m.input.SetValue(row.item.Symbol)
	m.input.CursorEnd()
	m.input.Focus()
}

func (m *Finance) handlePromptKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = fmList
		m.input.Blur()
		return m, nil
	case "enter":
		raw := strings.TrimSpace(m.input.Value())
		resolved, err := finance.Resolve(raw)
		if err != nil {
			m.promptErr = err.Error()
			return m, nil
		}
		m.input.Blur()
		mode := m.mode
		m.mode = fmList
		if mode == fmAddPrompt {
			return m, m.addItem(resolved.DisplayHint, resolved.Kind, resolved.Canonical)
		}
		return m, m.updateItem(m.editingID, resolved.DisplayHint, resolved.Kind, resolved.Canonical)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m *Finance) handleConfirmKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Reserved — current delete flow uses two-step inside list mode. Kept for
	// future modal-style confirms.
	return m.handleListKey(k)
}

func (m *Finance) handleDetailKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		m.mode = fmList
		m.detail = nil
		m.detailNews = nil
		m.detailErr = ""
		return m, nil
	case "r":
		if m.rowCursor >= 0 && m.rowCursor < len(m.rows) {
			m.detailLoading = true
			m.detailErr = ""
			return m, m.loadDetail(m.rows[m.rowCursor].item)
		}
	}
	return m, nil
}

func (m *Finance) setStatus(msg string, kind statusKind) {
	m.statusMsg = msg
	m.statusKind = kind
}

func (m *Finance) clearStatus() {
	m.statusMsg = ""
}

// ──────────── View ────────────

var (
	financeTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	financeHint  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	financeHdr   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted))
	financeSym   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	financeType  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	financePx    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorText))
	financeUp    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorGreen))
	financeDown  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	financeErr   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	financeOK    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorGreen)).Bold(true)
	financeWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed)).Bold(true)
	financeCur   = lipgloss.NewStyle().Background(lipgloss.Color(theme.ColorSurfaceAlt))
	financeNews  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText))
	financeAge   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
)

func (m *Finance) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	if m.mode == fmDetail {
		return m.viewDetail()
	}
	return m.viewList()
}

func (m *Finance) viewList() string {
	var b strings.Builder

	hint := "watchlist · A add · E edit · D del · K/J move · Enter detail · N news · R refresh · Esc back"
	b.WriteString(financeTitle.Render("Finance") + "  " + financeHint.Render(hint))
	b.WriteString("\n\n")

	switch {
	case m.loading && len(m.rows) == 0:
		b.WriteString(financeHint.Render("loading watchlist…"))
		return b.String()
	case m.loadErr != "":
		b.WriteString(financeErr.Render("! " + m.loadErr))
		b.WriteString("\n\n")
		b.WriteString(financeHint.Render("press r to retry"))
		return b.String()
	}

	// Column header — uses the same width-aware cell helpers as the rows so
	// SYMBOL/TYPE/PRICE/CHG/%/SPARK line up exactly above their values.
	b.WriteString(financeHdr.Render(joinRow(
		padLeft(colSymW, "SYMBOL"),
		padLeft(colTypeW, "TYPE"),
		padRight(colPriceW, "PRICE"),
		padRight(colChgW, "CHG"),
		padRight(colPctW, "%"),
		padLeft(colSparkW, "SPARK"),
	)))
	b.WriteString("\n")

	for i, row := range m.rows {
		line := m.formatRow(row, sparklineWidth)
		if m.focus == ffRows && i == m.rowCursor {
			line = financeCur.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	if len(m.rows) == 0 {
		b.WriteString(financeHint.Render("(empty — press A to add a symbol)"))
		b.WriteString("\n")
	}

	if m.mode == fmAddPrompt || m.mode == fmEditPrompt {
		b.WriteString("\n")
		b.WriteString(financeHint.Render(m.promptHint))
		b.WriteString("\n")
		b.WriteString(m.input.View())
		b.WriteString("\n")
		if m.promptErr != "" {
			b.WriteString(financeErr.Render("! " + m.promptErr))
			b.WriteString("\n")
		}
	}

	// News pane.
	if len(m.news) > 0 {
		b.WriteString("\n")
		b.WriteString(financeHdr.Render("── finance news ────────────────────────────────────────────────"))
		b.WriteString("\n")
		newsLimit := 8
		if len(m.news) < newsLimit {
			newsLimit = len(m.news)
		}
		for i := 0; i < newsLimit; i++ {
			h := m.news[i]
			age := humanizeAge(time.Since(h.Published))
			line := fmt.Sprintf("  %s  %s", truncate(h.Title, 70), financeAge.Render("("+age+")"))
			if m.focus == ffNews && i == m.newsCursor {
				line = financeCur.Render(line)
			} else {
				line = financeNews.Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	// Status line.
	b.WriteString("\n")
	b.WriteString(m.statusLine())
	return b.String()
}

func (m *Finance) statusLine() string {
	if m.statusMsg != "" {
		var sty lipgloss.Style
		switch m.statusKind {
		case stSuccess:
			sty = financeOK
		case stWarning:
			sty = financeWarn
		default:
			sty = financeHint
		}
		return sty.Render(m.statusMsg)
	}
	loaded := 0
	for _, r := range m.rows {
		if r.quote != nil {
			loaded++
		}
	}
	stamp := m.sess.DisplayPrefs.FormatClockWithSeconds(time.Now())
	return financeHint.Render(fmt.Sprintf("%d/%d quotes · %d news · updated %s",
		loaded, len(m.rows), len(m.news), stamp))
}

// Column widths — used by both the header and each row so the table lines up
// regardless of ANSI escapes or wide unicode (▲/▼) inside any cell. Visible
// (not byte) widths.
const (
	colSymW   = 9
	colTypeW  = 6
	colPriceW = 12
	colChgW   = 12
	colPctW   = 8
	colSparkW = 12
)

// padLeft / padRight pad to a visible width, counting unicode + ANSI escapes
// correctly. fmt's "%Ns" verb counts bytes, which breaks both for styled
// strings (ANSI is invisible-but-bytes) and for wide glyphs like ▲/▼ (3 bytes
// but 1 cell). lipgloss's Width()+Align() measures visible width.
func padLeft(w int, s string) string {
	return lipgloss.NewStyle().Width(w).Align(lipgloss.Left).Render(s)
}

func padRight(w int, s string) string {
	return lipgloss.NewStyle().Width(w).Align(lipgloss.Right).Render(s)
}

// joinRow joins cells with a single space separator. Pulled out so every row
// (and the header) uses the same gap width — adjust here, not at each call.
func joinRow(cells ...string) string {
	return strings.Join(cells, " ")
}

func (m *Finance) formatRow(r financeRow, sparkW int) string {
	sym := truncate(r.item.Symbol, colSymW)
	kindStr := finance.Kind(r.item.Kind).String()

	if r.quote == nil {
		dash := financeHint.Render("—")
		return joinRow(
			padLeft(colSymW, financeSym.Render(sym)),
			padLeft(colTypeW, financeType.Render(kindStr)),
			padRight(colPriceW, dash),
			padRight(colChgW, dash),
			padRight(colPctW, dash),
			padLeft(colSparkW, dash),
		)
	}

	q := r.quote
	priceStr := formatPrice(q.PriceUSD)
	chgStyle := financeUp
	arrow := "▲"
	chgPrefix := "+"
	if q.Change24hUSD < 0 {
		chgStyle = financeDown
		arrow = "▼"
		chgPrefix = ""
	}
	chgInner := chgStyle.Render(fmt.Sprintf("%s %s%s", arrow, chgPrefix, formatChange(q.Change24hUSD)))
	pctInner := chgStyle.Render(fmt.Sprintf("%s%.2f%%", chgPrefix, q.Change24hPct))
	sparkStr := components.Sparkline(r.spark, sparkW)
	if sparkStr == "" {
		sparkStr = financeHint.Render("—")
	}

	return joinRow(
		padLeft(colSymW, financeSym.Render(sym)),
		padLeft(colTypeW, financeType.Render(kindStr)),
		padRight(colPriceW, financePx.Render(priceStr)),
		padRight(colChgW, chgInner),
		padRight(colPctW, pctInner),
		padLeft(colSparkW, sparkStr),
	)
}

func (m *Finance) viewDetail() string {
	var b strings.Builder
	hint := "R refresh · Esc back"
	b.WriteString(financeTitle.Render("Finance ▸ "+m.detailSym) + "  " + financeHint.Render(hint))
	b.WriteString("\n\n")
	switch {
	case m.detailLoading:
		b.WriteString(financeHint.Render("loading detail…"))
		return b.String()
	case m.detailErr != "":
		b.WriteString(financeErr.Render("! " + m.detailErr))
		return b.String()
	case m.detail == nil:
		b.WriteString(financeHint.Render("no data"))
		return b.String()
	}
	d := m.detail

	// Header line.
	arrow, sty := "▲", financeUp
	chgPrefix := "+"
	if d.Change24hUSD < 0 {
		arrow, sty, chgPrefix = "▼", financeDown, ""
	}
	header := fmt.Sprintf("%s — %s  %s %s%s (%s%.2f%%)",
		financeSym.Render(d.Display),
		financePx.Render(formatPrice(d.PriceUSD)),
		sty.Render(arrow),
		sty.Render(chgPrefix), sty.Render(formatChange(d.Change24hUSD)),
		sty.Render(chgPrefix), d.Change24hPct,
	)
	b.WriteString(header)
	b.WriteString("\n\n")

	// 10-row chart.
	w := m.sess.Width - 4
	if w < 20 {
		w = 20
	}
	if w > 100 {
		w = 100
	}
	for _, row := range components.BigChart(d.Series, w, 10) {
		b.WriteString(row)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Stats line — branched by kind.
	b.WriteString(m.detailStatsLine(d))
	b.WriteString("\n")

	// Related news.
	if len(m.detailNews) > 0 {
		b.WriteString("\n")
		b.WriteString(financeHdr.Render("── related news ─────────────────────────────────"))
		b.WriteString("\n")
		for _, h := range m.detailNews {
			age := humanizeAge(time.Since(h.Published))
			b.WriteString(financeNews.Render("  " + truncate(h.Title, 70)))
			b.WriteString("  ")
			b.WriteString(financeAge.Render("(" + age + ")"))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m *Finance) detailStatsLine(d *finance.Detail) string {
	var parts []string
	if d.Open != nil {
		parts = append(parts, "Open "+formatPrice(*d.Open))
	}
	switch m.detailKind {
	case finance.KindStock:
		if d.DayLow != nil && d.DayHigh != nil {
			parts = append(parts, fmt.Sprintf("Day %s–%s", formatPrice(*d.DayLow), formatPrice(*d.DayHigh)))
		}
		if d.Week52Low != nil && d.Week52High != nil {
			parts = append(parts, fmt.Sprintf("52w %s–%s", formatPrice(*d.Week52Low), formatPrice(*d.Week52High)))
		}
		if d.Volume != nil {
			parts = append(parts, "Vol "+humanizeWithCommas(float64(*d.Volume), 0))
		}
	case finance.KindCrypto:
		if d.DayLow != nil && d.DayHigh != nil {
			parts = append(parts, fmt.Sprintf("Day %s–%s", formatPrice(*d.DayLow), formatPrice(*d.DayHigh)))
		}
		if d.MarketCapUSD > 0 {
			parts = append(parts, "Mkt cap "+formatBigUSD(d.MarketCapUSD))
		}
	case finance.KindFx:
		if d.Week52Low != nil && d.Week52High != nil {
			parts = append(parts, fmt.Sprintf("1y range %s–%s", formatPrice(*d.Week52Low), formatPrice(*d.Week52High)))
		}
	}
	if len(parts) == 0 {
		return financeHint.Render("(no stats available)")
	}
	return financeHint.Render(strings.Join(parts, "  "))
}

// ──────────── formatters ────────────

// formatPrice picks digit counts so $0.0042 still shows precision while
// $108,432.18 stays readable. Three-tier heuristic, matches what the .NET
// FinanceScreen does.
func formatPrice(p float64) string {
	switch {
	case p >= 100:
		return "$" + humanizeWithCommas(p, 2)
	case p >= 1:
		return fmt.Sprintf("$%.2f", p)
	case p >= 0.01:
		return fmt.Sprintf("$%.4f", p)
	default:
		return fmt.Sprintf("$%.6f", p)
	}
}

// formatChange is like formatPrice but without the leading "$" and without a
// sign — the caller adds the +/- prefix and color.
func formatChange(c float64) string {
	abs := c
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs >= 100:
		return humanizeWithCommas(abs, 2)
	case abs >= 1:
		return fmt.Sprintf("%.2f", abs)
	default:
		return fmt.Sprintf("%.4f", abs)
	}
}

// humanizeWithCommas formats a non-negative float with US-style thousands
// separators on the integer portion. Go's fmt has no native ',' verb, so this
// is the workaround; price values are always non-negative so we don't handle
// the sign here.
func humanizeWithCommas(v float64, decimals int) string {
	s := strconv.FormatFloat(v, 'f', decimals, 64)
	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	n := len(intPart)
	for i, ch := range intPart {
		if i > 0 && (n-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	if hasFrac {
		b.WriteByte('.')
		b.WriteString(fracPart)
	}
	return b.String()
}

// formatBigUSD compacts large numbers: 1.4B, 423M, 23.1K.
func formatBigUSD(v float64) string {
	switch {
	case v >= 1e12:
		return fmt.Sprintf("$%.2fT", v/1e12)
	case v >= 1e9:
		return fmt.Sprintf("$%.2fB", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("$%.1fM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("$%.1fK", v/1e3)
	}
	return fmt.Sprintf("$%.2f", v)
}

func humanizeAge(d time.Duration) string {
	if d < time.Minute {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return s[:n-1] + "…"
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
