package screens

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/cards"
	"github.com/nickna/ssh.night.ms/internal/doors/holdem/multiplayer"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// HoldemMP is the multiplayer Hold'em UI. Lifecycle:
//
//	hmpLobby:  list tables, 'n' to create, Enter to join.
//	hmpTable:  subscribed to one Coordinator; render TableSnapshots + accept
//	           F/C/R/A actions.  Esc stands up + returns to the lobby.
type HoldemMP struct {
	sess *session.Session

	mode hmpMode
	err  string

	// lobby state
	tables      []multiplayer.TableInfo
	tableCursor int

	// table state
	activeID  int64
	coord     *multiplayer.Coordinator
	snap      multiplayer.TableSnapshot
	sub       <-chan multiplayer.TableSnapshot
	cancelSub func()

	// buy-in for the next join.
	buyIn int32

	// wallet snapshot for displaying "your chips" + the buy-in availability.
	wallet  doors.Wallet
	loading bool
}

type hmpMode int

const (
	hmpLobby hmpMode = iota
	hmpTable
)

func NewHoldemMP(sess *session.Session) tea.Model {
	return &HoldemMP{sess: sess, mode: hmpLobby, buyIn: 100, loading: true}
}

// tea.Msg envelopes

type hmpWalletMsg struct {
	wallet doors.Wallet
	err    error
}
type hmpTablesMsg struct{ tables []multiplayer.TableInfo }
type hmpSnapMsg struct{ snap multiplayer.TableSnapshot }
type hmpBuyInMsg struct {
	wallet doors.Wallet
	err    error
}

func (m *HoldemMP) Init() tea.Cmd {
	return tea.Batch(m.loadWallet(), m.refreshTables())
}

func (m *HoldemMP) loadWallet() tea.Cmd {
	user := m.sess.Identity.UserID
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3 * time.Second)
		defer cancel()
		w, err := svc.Load(ctx, user)
		return hmpWalletMsg{wallet: w, err: err}
	}
}

func (m *HoldemMP) refreshTables() tea.Cmd {
	reg := m.sess.HoldemRegistry
	return func() tea.Msg {
		if reg == nil {
			return hmpTablesMsg{tables: nil}
		}
		return hmpTablesMsg{tables: reg.List()}
	}
}

// waitSnap blocks on the subscription channel and re-fires itself so the
// screen keeps re-rendering as the coordinator broadcasts state changes.
func waitSnap(ch <-chan multiplayer.TableSnapshot) tea.Cmd {
	return func() tea.Msg {
		snap, ok := <-ch
		if !ok {
			return nil
		}
		return hmpSnapMsg{snap: snap}
	}
}

func (m *HoldemMP) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case hmpWalletMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.wallet = msg.wallet

	case hmpTablesMsg:
		m.tables = msg.tables
		m.tableCursor = clampIndex(m.tableCursor, len(m.tables))

	case hmpSnapMsg:
		m.snap = msg.snap
		return m, waitSnap(m.sub)

	case hmpBuyInMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.wallet = msg.wallet

	case tea.KeyMsg:
		if m.mode == hmpTable {
			return m.tableUpdate(msg)
		}
		return m.lobbyUpdate(msg)
	}
	return m, nil
}

func (m *HoldemMP) lobbyUpdate(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		return m, nav.Navigate(nav.DestDoors)
	case "r":
		return m, m.refreshTables()
	case "J":
		// Quick-rejoin: if the user is already seated at any active table,
		// hop right back into it without re-debiting the wallet. Lets a
		// disconnect-mid-hand recover by reconnecting via SSH + 'd' +
		// "Hold'em Multiplayer" + Shift+J. Lowercase 'j' stays as the
		// vim-style cursor-down binding.
		return m.rejoinExistingSeat()
	case "n":
		// Create a new table with sensible defaults — 6-max, BB=10. The
		// creator doesn't auto-join; they pick a seat after the table is
		// listed.
		reg := m.sess.HoldemRegistry
		if reg == nil {
			m.err = "multiplayer registry not configured"
			return m, nil
		}
		name := fmt.Sprintf("%s's table", m.sess.Identity.Handle)
		coord := reg.Create(name, 6, 5, 10)
		m.activeID = coord.TableID
		return m.joinTable(coord)
	case "up", "k":
		if m.tableCursor > 0 {
			m.tableCursor--
		}
	case "down", "j":
		if m.tableCursor < len(m.tables)-1 {
			m.tableCursor++
		}
	case "left", "h":
		if m.buyIn > 50 {
			m.buyIn -= 50
		}
	case "right", "l":
		if m.buyIn < 5000 {
			m.buyIn += 50
		}
	case "enter":
		if len(m.tables) == 0 || m.tableCursor >= len(m.tables) {
			return m, nil
		}
		reg := m.sess.HoldemRegistry
		if reg == nil {
			return m, nil
		}
		coord := reg.Get(m.tables[m.tableCursor].ID)
		if coord == nil {
			m.err = "table is gone"
			return m, m.refreshTables()
		}
		return m.joinTable(coord)
	}
	return m, nil
}

// rejoinExistingSeat scans the registry for a table where the current user
// already has a seat and resumes their view without buying back in. The
// snapshot is the coordinator's authoritative state, so chip stacks +
// the hole/board cards repopulate exactly as they were.
func (m *HoldemMP) rejoinExistingSeat() (tea.Model, tea.Cmd) {
	reg := m.sess.HoldemRegistry
	if reg == nil {
		m.err = "multiplayer registry not configured"
		return m, nil
	}
	for _, info := range reg.List() {
		coord := reg.Get(info.ID)
		if coord == nil {
			continue
		}
		ch, cancel := coord.Subscribe(m.sess.Identity.UserID)
		// Pull the first snapshot synchronously inside a short window so we
		// can confirm a seat actually exists before committing to table
		// mode.
		var snap multiplayer.TableSnapshot
		select {
		case snap = <-ch:
		case <-time.After(800 * time.Millisecond):
			cancel()
			continue
		}
		seated := false
		for _, s := range snap.Seats {
			if s.UserID == m.sess.Identity.UserID {
				seated = true
				break
			}
		}
		if !seated {
			cancel()
			continue
		}
		m.coord = coord
		m.activeID = coord.TableID
		m.snap = snap
		m.sub = ch
		m.cancelSub = cancel
		m.mode = hmpTable
		m.err = ""
		return m, waitSnap(m.sub)
	}
	m.err = "no seats to rejoin — press 'n' to create a table or pick one to join"
	return m, nil
}

// joinTable subscribes + tries to sit in the first open seat. Caller is
// expected to have ensured the user has enough chips for the buy-in.
func (m *HoldemMP) joinTable(coord *multiplayer.Coordinator) (tea.Model, tea.Cmd) {
	if m.wallet.Total() < int64(m.buyIn) {
		m.err = fmt.Sprintf("need %d chips to buy in (have %d)", m.buyIn, m.wallet.Total())
		return m, nil
	}
	m.coord = coord
	m.activeID = coord.TableID
	m.mode = hmpTable
	m.err = ""

	// Debit the buy-in. Settlement on stand returns whatever chips remain.
	bet := m.buyIn
	wallet := m.wallet
	user := m.sess.Identity.UserID
	handle := m.sess.Identity.Handle
	svc := m.sess.Wallet

	debitCmd := func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		if err := svc.Bet(ctx, &wallet, bet); err != nil {
			return hmpBuyInMsg{err: err}
		}
		return hmpBuyInMsg{wallet: wallet}
	}

	// Find an open seat client-side first; coordinator double-checks.
	snap := m.snap
	if snap.TableID == 0 {
		// Pull a fresh snap via Subscribe init.
		ch, cancel := coord.Subscribe(user)
		m.sub = ch
		m.cancelSub = cancel
		// Wait for first snap synchronously inside a Cmd. Actor-side delay
		// is sub-second; we return a no-op Init cmd that consumes one.
	}
	_ = handle

	// Coordinator picks first open seat for us.
	seatIdx := -1
	for i, s := range snap.Seats {
		if s.UserID == 0 {
			seatIdx = i
			break
		}
	}
	if seatIdx < 0 {
		// Default to 0; the Coordinator returns false from Sit if it's taken
		// and the screen surfaces an error.
		seatIdx = 0
	}
	sitOk := coord.Sit(user, handle, seatIdx, bet)
	if !sitOk {
		// Try linear scan if the snapshot was empty / first seat is taken.
		for i := 0; i < 9; i++ {
			if coord.Sit(user, handle, i, bet) {
				sitOk = true
				break
			}
		}
	}
	if !sitOk {
		m.err = "couldn't take a seat"
		m.mode = hmpLobby
		return m, debitCmd
	}
	if m.sub == nil {
		ch, cancel := coord.Subscribe(user)
		m.sub = ch
		m.cancelSub = cancel
	}
	return m, tea.Batch(debitCmd, waitSnap(m.sub))
}

func (m *HoldemMP) tableUpdate(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		return m.leaveTable()
	case "f":
		_ = m.coord.Action(m.sess.Identity.UserID, multiplayer.ActFold)
	case "c":
		_ = m.coord.Action(m.sess.Identity.UserID, multiplayer.ActCheckCall)
	case "r":
		_ = m.coord.Action(m.sess.Identity.UserID, multiplayer.ActRaise)
	case "a":
		_ = m.coord.Action(m.sess.Identity.UserID, multiplayer.ActAllIn)
	}
	return m, nil
}

func (m *HoldemMP) leaveTable() (tea.Model, tea.Cmd) {
	if m.coord == nil {
		return m, nil
	}
	chips := m.coord.Stand(m.sess.Identity.UserID)
	if m.cancelSub != nil {
		m.cancelSub()
	}
	m.cancelSub = nil
	m.sub = nil
	m.coord = nil
	m.activeID = 0
	m.snap = multiplayer.TableSnapshot{}
	m.mode = hmpLobby

	// Credit the chips back to the wallet. The per-session game_rounds row
	// the legacy stack used to write here is no longer recorded —
	// multiplayer audit is now per-hand via the registry's settlement
	// ledger, so a session-grain row would double-count on leaderboards.
	wallet := m.wallet
	svc := m.sess.Wallet
	creditCmd := func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		if chips > 0 {
			_ = svc.Credit(ctx, &wallet, int64(chips))
		}
		return hmpBuyInMsg{wallet: wallet}
	}
	return m, tea.Batch(creditCmd, m.refreshTables())
}

var (
	hmpTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	hmpHint     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	hmpCard     = lipgloss.NewStyle().Bold(true).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(theme.ColorAccent)).Padding(0, 1)
	hmpCardBack = lipgloss.NewStyle().Bold(true).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(theme.ColorDim)).Padding(0, 1)
	hmpLabel    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	hmpWin      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	hmpErr      = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	hmpAct      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorGreen))
)

func (m *HoldemMP) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	if m.loading {
		return hmpHint.Render("loading wallet…")
	}
	if m.mode == hmpTable {
		return m.viewTable()
	}
	return m.viewLobby()
}

func (m *HoldemMP) viewLobby() string {
	var b strings.Builder
	b.WriteString(hmpTitle.Render("Hold'em Tables"))
	b.WriteString("  ")
	b.WriteString(hmpHint.Render("↑/↓ select · Enter join · n new · ←/→ buy-in · r refresh · Esc back"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("  wallet:  %d chips    buy-in:  %d\n\n", m.wallet.Total(), m.buyIn))

	if m.err != "" {
		b.WriteString(hmpErr.Render("! "+m.err) + "\n\n")
	}
	if len(m.tables) == 0 {
		b.WriteString(hmpHint.Render("no tables yet — press 'n' to create one."))
		return b.String()
	}
	b.WriteString(hmpHint.Render("(Shift+J) rejoin if you were disconnected mid-hand"))
	b.WriteString("\n\n")
	for i, t := range m.tables {
		prefix := "  "
		if i == m.tableCursor {
			prefix = "▸ "
		}
		row := fmt.Sprintf("%s#%d  %-32s  seats %d/%d  BB %d",
			prefix, t.ID, t.Name, t.Occupied, t.CapSeats, t.BB)
		b.WriteString(row)
		b.WriteString("\n")
	}
	return b.String()
}

func (m *HoldemMP) viewTable() string {
	var b strings.Builder
	b.WriteString(hmpTitle.Render(fmt.Sprintf("Table #%d", m.activeID)))
	b.WriteString("  ")
	b.WriteString(hmpHint.Render("F fold · C check/call · R raise · A all-in · Esc leave"))
	b.WriteString("\n\n")
	if m.err != "" {
		b.WriteString(hmpErr.Render("! "+m.err) + "\n\n")
	}

	// Render every seat with handle + stack + bet. Highlight the to-act seat.
	for i, s := range m.snap.Seats {
		var line strings.Builder
		marker := "  "
		if i == m.snap.Button {
			marker = "B "
		}
		if i == m.snap.ToAct && m.snap.HandRunning {
			marker = hmpAct.Render("→ ")
		}
		line.WriteString(marker)
		if s.UserID == 0 {
			line.WriteString(hmpHint.Render(fmt.Sprintf("seat %d  <empty>", i)))
		} else {
			label := fmt.Sprintf("seat %d  %-16s  stack %4d  bet %3d", i, truncate(s.Handle, 16), s.ChipsHand, s.Bet)
			if s.Folded {
				line.WriteString(hmpHint.Render(label + "  (folded)"))
			} else if s.AllIn {
				line.WriteString(hmpAct.Render(label + "  (all-in)"))
			} else {
				line.WriteString(hmpLabel.Render(label))
			}
			line.WriteString("  ")
			if s.HoleShown {
				line.WriteString(hmpCard.Render(s.Hole[0].String()) + " ")
				line.WriteString(hmpCard.Render(s.Hole[1].String()))
			} else if s.UserID != 0 && m.snap.HandRunning {
				line.WriteString(hmpCardBack.Render("??") + " " + hmpCardBack.Render("??"))
			}
		}
		b.WriteString(line.String())
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString("  " + hmpLabel.Render(fmt.Sprintf("Board (%s · pot %d)", m.snap.Street.String(), m.snap.Pot)))
	b.WriteString("\n  ")
	for _, c := range m.snap.Board {
		b.WriteString(hmpCard.Render(c.String()) + " ")
	}
	for i := len(m.snap.Board); i < 5; i++ {
		b.WriteString(hmpCardBack.Render("  ") + " ")
	}
	b.WriteString("\n\n")

	if !m.snap.HandRunning {
		if m.snap.Winner >= 0 {
			b.WriteString("  " + hmpWin.Render(fmt.Sprintf("Seat %d wins with %s — next hand soon…", m.snap.Winner, m.snap.WinRank.String())))
		} else if m.snap.OccupiedSeats >= 2 {
			b.WriteString("  " + hmpHint.Render("split pot — next hand soon…"))
		} else {
			b.WriteString("  " + hmpHint.Render("waiting for more players…"))
		}
	} else if m.snap.ToAct == m.indexOfSelf() {
		owe := int32(0)
		for _, s := range m.snap.Seats {
			if s.Bet > owe {
				owe = s.Bet
			}
		}
		owe -= m.snap.Seats[m.snap.ToAct].Bet
		if owe < 0 {
			owe = 0
		}
		if owe > 0 {
			b.WriteString("  " + hmpAct.Render(fmt.Sprintf("Your move — to call: %d  (F/C/R/A)", owe)))
		} else {
			b.WriteString("  " + hmpAct.Render("Your move — no bet to call  (F/C/R/A)"))
		}
	} else {
		b.WriteString("  " + hmpHint.Render("Waiting…"))
	}
	return b.String()
}

// indexOfSelf finds the snapshot seat index for the current user, or -1.
func (m *HoldemMP) indexOfSelf() int {
	for i, s := range m.snap.Seats {
		if s.UserID == m.sess.Identity.UserID {
			return i
		}
	}
	return -1
}

// Keep cards.Card referenced even when not all code paths touch it directly.
var _ = cards.Ace
