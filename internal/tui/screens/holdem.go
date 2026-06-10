package screens

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/cards"
	"github.com/nickna/ssh.night.ms/internal/doors/holdem"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Holdem is a heads-up cash table vs one CPU bot. v1: single buy-in pulled
// from the wallet (daily-bucket first, then winnings), playable until you
// quit; whatever stack you end with is credited back. Chip movement during
// the hand stays inside the Game state — no per-action wallet writes.
type Holdem struct {
	sess    *session.Session
	wallet  doors.Wallet
	bet     int32 // buy-in amount (= starting stack)
	loading bool
	lastErr string

	game           *holdem.Game
	cashedOut      bool
	cashedOutValue int32

	// Animation state. One DealAnimation field handles both phases — the
	// hole-card deal at hand start and the per-street community-card
	// deal — distinguished by dealStage so the renderer knows whether
	// deal.Revealed bounds hole-card visibility or board visibility.
	deal            components.DealAnimation
	dealStage       heDealStage
	boardRevealFrom int // board index where the in-flight board deal began
	pulse           components.PulseAnimation
	shower          components.CoinShower
	lastWinAt       int64 // hand counter that already triggered shower; prevents re-fire
	handCount       int64

	// bestFive holds the 5 indices (into [hole[0], hole[1], board[0..4]])
	// of the winner's winning subset. Populated at showdown by advance()
	// once the hand has ended; nil otherwise.
	bestFive []int
}

type heDealStage int

const (
	heDealNone heDealStage = iota
	heDealHole
	heDealBoard
)

// heAdvanceMsg drives the bot/showdown state machine — one step per
// message so per-street deals can pause between bot actions. tea.Tick
// adds a short pause so the bot doesn't blur through its actions all in
// one frame.
type heAdvanceMsg struct{}

func NewHoldem(sess *session.Session) tea.Model {
	return &Holdem{sess: sess, bet: 50, loading: true}
}

type heWalletMsg struct {
	wallet doors.Wallet
	err    error
}

type heBetMsg struct {
	wallet doors.Wallet
	err    error
}

type heCashOutMsg struct {
	wallet doors.Wallet
	err    error
}

func (m *Holdem) Init() tea.Cmd {
	user := m.sess.Identity.UserID
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3 * time.Second)
		defer cancel()
		w, err := svc.Load(ctx, user)
		return heWalletMsg{wallet: w, err: err}
	}
}

func (m *Holdem) buyInCmd() tea.Cmd {
	if m.wallet.Total() < int64(m.bet) {
		m.lastErr = fmt.Sprintf("not enough credits — buy-in %d, you have %d", m.bet, m.wallet.Total())
		return nil
	}
	bet := m.bet
	wallet := m.wallet
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3 * time.Second)
		defer cancel()
		if err := svc.Bet(ctx, &wallet, bet); err != nil {
			return heBetMsg{err: err}
		}
		return heBetMsg{wallet: wallet}
	}
}

func (m *Holdem) cashOutCmd() tea.Cmd {
	if m.game == nil {
		return nil
	}
	stacks := m.game.Stacks()
	final := stacks[holdem.SeatPlayer]
	user := m.sess.Identity.UserID
	wallet := m.wallet
	svc := m.sess.Wallet
	bet := m.bet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3 * time.Second)
		defer cancel()
		if final > 0 {
			if err := svc.Credit(ctx, &wallet, int64(final)); err != nil {
				return heCashOutMsg{wallet: wallet, err: err}
			}
		}
		_ = svc.Record(ctx, doors.LedgerEntry{
			UserID: user, GameKey: "holdem",
			Bet:    bet,
			Payout: final,
			Net:    final - bet,
			Details: map[string]any{
				"final_stack": final,
				"buyin":       bet,
			},
		})
		return heCashOutMsg{wallet: wallet}
	}
}

func (m *Holdem) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case heWalletMsg:
		m.loading = false
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		m.wallet = msg.wallet
	case heBetMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		m.lastErr = ""
		m.wallet = msg.wallet
		// Start fresh table.
		bb := m.bet / 50 // 100 buy-in → 2 BB; cheap default
		if bb < 2 {
			bb = 2
		}
		sb := bb / 2
		m.game = holdem.New(doors.CryptoRng{}, m.bet, sb, bb, holdem.SeatBot)
		return m, m.newHandCmd()
	case heCashOutMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		m.wallet = msg.wallet
		m.cashedOut = true
		m.cashedOutValue = m.game.Stacks()[holdem.SeatPlayer]
		m.game = nil

	case components.DealTickMsg:
		m.deal.Step()
		if !m.deal.Done() {
			return m, m.deal.Tick()
		}
		// Animation just completed — clear the stage and resume the
		// advance loop (bot pre-flop action, post-action bot replies,
		// or showdown payout, all handled inside advance).
		m.dealStage = heDealNone
		return m, m.advanceCmd()
	case heAdvanceMsg:
		return m, m.advance()
	case components.PulseTickMsg:
		m.pulse.Step()
		if !m.pulse.Done() {
			return m, m.pulse.Tick()
		}
	case components.CoinShowerTickMsg:
		m.shower.Step()
		if !m.shower.Done() {
			return m, m.shower.Tick()
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			if m.game != nil && !m.cashedOut {
				// Cash out before leaving so the player's chips return to the
				// wallet. (Skip if they never bought in.)
				return m, tea.Batch(m.cashOutCmd(), nav.Navigate(nav.DestDoors))
			}
			return m, nav.Navigate(nav.DestDoors)
		case "left":
			if m.game == nil {
				m.bet = stepBuyIn(m.bet, -1)
			}
		case "right":
			if m.game == nil {
				m.bet = stepBuyIn(m.bet, +1)
			}
		case " ":
			if m.game == nil {
				return m, m.buyInCmd()
			}
			if !m.game.HandActive() && m.deal.Done() {
				// Pot already awarded; start next hand on this table.
				m.game.AdvanceButton()
				return m, m.newHandCmd()
			}
		case "f":
			return m, m.applyPlayer(holdem.ActFold)
		case "c":
			return m, m.applyPlayer(holdem.ActCheckCall)
		case "r":
			return m, m.applyPlayer(holdem.ActRaise)
		case "a":
			return m, m.applyPlayer(holdem.ActAllIn)
		case "x":
			// Cash out manually (rebuy-or-leave UX is just esc + space; this
			// is for safe leave without re-entering the menu).
			if m.game != nil && !m.cashedOut {
				return m, m.cashOutCmd()
			}
		}
	}
	return m, nil
}

// applyPlayer issues a player action, then defers all follow-up bot
// activity to the advance state machine. If the player's action grew
// the board (e.g. a check that closes the round), the board deal
// animation runs first; otherwise the state machine resumes
// immediately.
func (m *Holdem) applyPlayer(a holdem.Action) tea.Cmd {
	if m.game == nil || !m.game.HandActive() || !m.deal.Done() {
		return nil
	}
	if m.game.ToAct() != holdem.SeatPlayer {
		return nil
	}
	prevBoard := len(m.game.Board())
	m.game.PlayerAction(a)
	return m.afterStateChange(prevBoard)
}

// newHandCmd starts a fresh hand on the current table and kicks off the
// 4-card hole-card deal animation. Bot pre-flop action (if any) is
// deferred to the advance state machine that runs after the hole-deal
// animation completes.
func (m *Holdem) newHandCmd() tea.Cmd {
	m.game.StartHand()
	m.handCount++
	m.bestFive = nil
	m.dealStage = heDealHole
	m.deal = components.NewDealAnimation(4)
	return m.deal.Tick()
}

// advance runs one step of the bot/showdown state machine: returns
// either a board-deal Cmd (if the next bot action just grew the
// board), another advance message (to keep the loop running), or nil
// (if it's the player's turn or the hand just ended with no pot to
// award the player).
func (m *Holdem) advance() tea.Cmd {
	if m.game == nil {
		return nil
	}
	if !m.game.HandActive() {
		// Hand is over — capture winning indices for the renderer,
		// then fire the pulse + shower if the player won.
		m.captureBestFive()
		return m.maybeWinCmd()
	}
	if m.game.ToAct() == holdem.SeatPlayer {
		// Wait for keypress.
		return nil
	}
	prevBoard := len(m.game.Board())
	m.game.StepBot()
	return m.afterStateChange(prevBoard)
}

// afterStateChange compares the board length before and after an action
// to decide whether to animate a new street; either way it eventually
// yields back into the advance loop.
func (m *Holdem) afterStateChange(prevBoard int) tea.Cmd {
	curBoard := len(m.game.Board())
	if curBoard > prevBoard {
		m.dealStage = heDealBoard
		m.boardRevealFrom = prevBoard
		m.deal = components.NewDealAnimation(curBoard - prevBoard)
		return m.deal.Tick()
	}
	return m.advanceCmd()
}

// advanceCmd schedules the next advance tick. A short pause between
// bot actions keeps the hand from blurring past — the player needs a
// beat to register what the bot did.
func (m *Holdem) advanceCmd() tea.Cmd {
	return tea.Tick(220*time.Millisecond, func(time.Time) tea.Msg { return heAdvanceMsg{} })
}

// captureBestFive computes the winner's best-5 indices into the
// 7-card layout [playerHole[0], playerHole[1], board[0..4]] or its
// bot equivalent. Indices match the layout slot order so the renderer
// can map them directly. Called once per hand, at hand end.
func (m *Holdem) captureBestFive() {
	if m.game == nil || m.game.HandActive() {
		return
	}
	winner := m.game.WinnerSeat()
	if winner != holdem.SeatPlayer && winner != holdem.SeatBot {
		// Split pot — no single best-5 to highlight.
		m.bestFive = nil
		return
	}
	board := m.game.Board()
	if len(board) < 3 {
		// Hand ended pre-flop (someone folded); no board to highlight.
		m.bestFive = nil
		return
	}
	var hole [2]cards.Card
	if winner == holdem.SeatPlayer {
		hole = m.game.PlayerHole()
	} else {
		bot, _ := m.game.BotHole()
		hole = bot
	}
	seven := make([]cards.Card, 0, 2+len(board))
	seven = append(seven, hole[0], hole[1])
	seven = append(seven, board...)
	_, _, idx := cards.EvaluateBestIndices(seven)
	m.bestFive = idx
}

// maybeWinCmd returns a pulse + shower command pair if the hand just
// resolved with the player taking the pot (and this hand hasn't already
// triggered the shower).
func (m *Holdem) maybeWinCmd() tea.Cmd {
	if m.game == nil || m.game.HandActive() {
		return nil
	}
	if m.game.WinnerSeat() != holdem.SeatPlayer {
		return nil
	}
	if m.lastWinAt == m.handCount {
		return nil
	}
	m.lastWinAt = m.handCount
	m.pulse = components.NewPulseAnimation(3)
	m.shower = components.NewCoinShower(heCabinetWidth-2, 12, time.Now().UnixNano())
	return tea.Batch(m.pulse.Tick(), m.shower.Tick())
}

func stepBuyIn(cur int32, dir int) int32 {
	ladder := []int32{50, 100, 200, 500, 1000, 2500, 5000}
	idx := 0
	for i, v := range ladder {
		if v == cur {
			idx = i
			break
		}
		if v < cur {
			idx = i
		}
	}
	idx += dir
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ladder) {
		idx = len(ladder) - 1
	}
	return ladder[idx]
}

// Inline body styles. Card sprites + cabinet chrome live in
// internal/tui/components (cardart.go, cabinet.go).
var (
	heHint    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	heWin     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	heBalance = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorGreen))
	heLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	heErr     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
)

const heCabinetWidth = 72 // wider than BJ/VP because of the 5-card board

func (m *Holdem) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	var body strings.Builder
	footer := "←/→ buy-in · Space deal/next · F/C/R/A · X cash out · Esc back"

	if m.loading {
		body.WriteString("\n" + heHint.Render("loading wallet…") + "\n")
	} else if m.game == nil {
		body.WriteString("\n")
		if m.cashedOut {
			body.WriteString(heWin.Render(fmt.Sprintf("Cashed out with %d credits.", m.cashedOutValue)) + "\n\n")
		}
		body.WriteString(heHint.Render("press Space to buy in") + "\n\n")
		body.WriteString(fmt.Sprintf("buy-in:  %s\n", heLabel.Render(fmt.Sprintf("%d", m.bet))))
		body.WriteString(fmt.Sprintf("total:   %s\n", heBalance.Render(fmt.Sprintf("%d", m.wallet.Total()))))
	} else {
		body.WriteString(m.renderTable())
	}
	body.WriteString("\n" + m.renderShowerBand())
	if m.lastErr != "" {
		body.WriteString("\n" + heErr.Render("! "+m.lastErr) + "\n")
	}

	walletBet := m.bet
	walletTotal := m.wallet.Total()
	if m.game != nil {
		walletBet = m.game.Stacks()[holdem.SeatPlayer]
		walletTotal = m.wallet.Total()
	}
	return components.CabinetFrame(body.String(), components.CabinetOpts{
		Title:      "Texas Hold'em",
		Width:      heCabinetWidth,
		FeltAccent: theme.ColorFeltHE,
		Wallet: components.CabinetWallet{
			Bet:    walletBet,
			Total:  walletTotal,
			Payout: m.payoutChip(),
		},
		Footer: footer,
	})
}

// renderTable renders the bot row + board + player row + status, with
// state-aware highlighting at showdown and per-street deal animation.
func (m *Holdem) renderTable() string {
	g := m.game
	stacks := g.Stacks()
	bets := g.Bets()
	playerHoleVisible, botHoleVisible := m.dealVisible()
	boardVisibleCount := m.boardVisible()

	// Pulse-aware "winning" state: alternates Winning/Normal while the
	// pulse animation is mid-cycle, so the winning cards visibly flash.
	winState := components.CardStateWinning
	if !m.pulse.Done() && !m.pulse.Bright() {
		winState = components.CardStateNormal
	}

	var b strings.Builder

	// Bot row.
	bot, reveal := g.BotHole()
	b.WriteString("\n" + heLabel.Render(fmt.Sprintf("Bot (stack %d, bet %d)", stacks[holdem.SeatBot], bets[holdem.SeatBot])) + "\n")
	b.WriteString(m.renderHoleRow(bot[:], reveal, botHoleVisible, holdem.SeatBot, winState))
	b.WriteString("\n\n")

	// Board.
	b.WriteString(heLabel.Render(fmt.Sprintf("Board (%s · pot %d)", g.Street().String(), g.Pot())) + "\n")
	b.WriteString(m.renderBoardRow(g.Board(), boardVisibleCount, winState))
	b.WriteString("\n\n")

	// Player row.
	p := g.PlayerHole()
	b.WriteString(heLabel.Render(fmt.Sprintf("You (stack %d, bet %d)", stacks[holdem.SeatPlayer], bets[holdem.SeatPlayer])) + "\n")
	b.WriteString(m.renderHoleRow(p[:], true, playerHoleVisible, holdem.SeatPlayer, winState))
	b.WriteString("\n\n")

	// Status line.
	b.WriteString(m.renderStatus())
	return b.String()
}

// dealVisible returns how many of each player's hole cards are
// currently visible during the hole-card deal animation. Once the
// hole-deal stage has completed (or if a board-deal is mid-flight),
// both hands show all of their hole cards.
func (m *Holdem) dealVisible() (player, bot int) {
	if m.deal.Done() || m.dealStage != heDealHole {
		return 2, 2
	}
	switch m.deal.Revealed {
	case 4, 3:
		return 2, m.deal.Revealed - 2
	case 2:
		return 1, 1
	case 1:
		return 1, 0
	}
	return 0, 0
}

// boardVisible returns how many community cards should currently
// render face-up. Outside a board-deal animation everything is shown;
// during one, only board[0..boardRevealFrom+deal.Revealed) is up.
func (m *Holdem) boardVisible() int {
	full := len(m.game.Board())
	if m.deal.Done() || m.dealStage != heDealBoard {
		return full
	}
	v := m.boardRevealFrom + m.deal.Revealed
	if v > full {
		v = full
	}
	return v
}

// renderHoleRow lays out two hole cards with per-card state pulled
// from cardStateAt. owner identifies which seat owns the hand so the
// state lookup can distinguish winner from loser at showdown.
func (m *Holdem) renderHoleRow(hole []cards.Card, reveal bool, visible int, owner int, winState components.CardState) string {
	if visible < 0 {
		visible = 0
	}
	if visible > 2 {
		visible = 2
	}
	sprites := make([]string, 0, 2)
	for i := 0; i < visible; i++ {
		if reveal {
			st := m.cardStateAt(owner, i, winState)
			sprites = append(sprites, components.RenderCard(hole[i], st))
		} else {
			sprites = append(sprites, components.RenderCardBack(components.CardStateNormal))
		}
	}
	return components.JoinCards(sprites...)
}

// renderBoardRow lays out the 5-slot community row. Slots already
// dealt past visibleCount stay face-up; slots beyond visibleCount but
// before len(board) render as empty placeholders so the row width
// stays constant (used during the brief deal-animation window when
// new cards are revealing).
func (m *Holdem) renderBoardRow(board []cards.Card, visibleCount int, winState components.CardState) string {
	sprites := make([]string, 0, 5)
	for i := 0; i < len(board); i++ {
		if i >= visibleCount {
			sprites = append(sprites, components.RenderCardEmpty())
			continue
		}
		// Board cards use slot index 2+i (slots 0,1 are the winner's
		// hole in the captured best-5 layout). Owner is irrelevant for
		// board lookups, so just pass SeatPlayer.
		st := m.cardStateAt(holdem.SeatPlayer, 2+i, winState)
		sprites = append(sprites, components.RenderCard(board[i], st))
	}
	for i := len(board); i < 5; i++ {
		sprites = append(sprites, components.RenderCardEmpty())
	}
	return components.JoinCards(sprites...)
}

// cardStateAt returns the CardState for a single card given its owner
// (SeatPlayer/SeatBot for hole cards; either is fine for board) and
// its slot index in the 7-card best-5 layout — slots 0/1 are the
// winner's hole cards, slots 2..6 are board[0..4]. Returns Normal
// during an active hand; at showdown applies the precise best-5
// highlighting captured in m.bestFive, falling back to a winner-takes-
// winState heuristic when best-5 wasn't captured (pre-flop folds).
func (m *Holdem) cardStateAt(owner int, slot int, winState components.CardState) components.CardState {
	if m.game == nil || m.game.HandActive() {
		return components.CardStateNormal
	}
	winner := m.game.WinnerSeat()
	if winner != holdem.SeatPlayer && winner != holdem.SeatBot {
		// Split pot — neither side highlighted.
		return components.CardStateNormal
	}
	if m.bestFive == nil {
		// Pre-flop fold or other no-board case: light up winner's
		// cards, leave board (if any) normal, dim loser's hole.
		if slot >= 2 {
			return components.CardStateNormal
		}
		if owner == winner {
			return winState
		}
		return components.CardStateDimmed
	}
	if slot >= 2 {
		if m.inBestFive(slot) {
			return winState
		}
		return components.CardStateDimmed
	}
	// Hole cards: only the winner's hole can sit in best-5.
	if owner == winner {
		if m.inBestFive(slot) {
			return winState
		}
		return components.CardStateDimmed
	}
	return components.CardStateDimmed
}

func (m *Holdem) inBestFive(slot int) bool {
	for _, i := range m.bestFive {
		if i == slot {
			return true
		}
	}
	return false
}

func (m *Holdem) renderStatus() string {
	g := m.game
	if !g.HandActive() {
		switch g.WinnerSeat() {
		case holdem.SeatPlayer:
			return heWin.Render(fmt.Sprintf("You win the pot with %s — Space for next hand.", g.WinRank().String()))
		case holdem.SeatBot:
			return heErr.Render(fmt.Sprintf("Bot wins with %s — Space for next hand.", g.WinRank().String()))
		default:
			return heHint.Render("Pot split — Space for next hand.")
		}
	}
	if g.ToAct() == holdem.SeatPlayer {
		toCall := g.ToCall()
		if toCall > 0 {
			return heLabel.Render(fmt.Sprintf("Your move — to call: %d (F/C/R/A)", toCall))
		}
		return heLabel.Render("Your move — no bet to call (F/C/R/A)")
	}
	return heHint.Render("Bot is thinking…")
}

func (m *Holdem) renderShowerBand() string {
	if !m.shower.Done() {
		return m.shower.Render()
	}
	return components.RenderBlank(heCabinetWidth-2, components.CoinShowerHeight)
}

// payoutChip returns the value to show in the wallet row's gold chip:
// the pot when the player just won a hand, 0 otherwise.
func (m *Holdem) payoutChip() int32 {
	if m.game == nil || m.game.HandActive() {
		return 0
	}
	if m.game.WinnerSeat() != holdem.SeatPlayer {
		return 0
	}
	return m.game.Pot()
}
