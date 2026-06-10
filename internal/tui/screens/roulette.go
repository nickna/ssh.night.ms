package screens

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/roulette"
	roulettemp "github.com/nickna/ssh.night.ms/internal/doors/roulette/multiplayer"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Cabinet width matches the other door games so the wallet row stays
// vertically aligned when the player tabs between Slots / Blackjack /
// Roulette in the same session.
const rouletteCabinetWidth = 60

// Animation cadence — fast scroll for 1.25s, eased deceleration for 2.5s,
// brief ball-landing pause, then optional pulse + coin shower. Total ribbon
// motion ≈ 3.75s, comfortably inside the coordinator's 5s Spinning phase.
const (
	rouletteSpinFastFrames     = 25
	rouletteSpinFastIntervalMs = 50
	rouletteSpinDecelFrames    = 25
	rouletteSpinDecelStepMs    = 6   // each decel frame adds 6ms to the wait
	rouletteWallClockTickMs    = 250 // for countdown refresh
)

// Chip ladder. The player's current chip size cycles through these via the
// 1/2/3/4 hotkeys (matching slots' bet-step UI). Defaults to 5 — the chunky
// middle option that lets a new player feel a $25 win without burning their
// entire allowance on one bad spin.
var rouletteChipLadder = []int32{1, 5, 25, 100}

// Roulette is the bubbletea screen for the global roulette table. State is
// driven by phase-msg broadcasts from the registry's coordinator; the
// screen overlays a locally-driven ribbon animation when the coordinator
// transitions into Spinning.
type Roulette struct {
	sess *session.Session

	coord     *roulettemp.Coordinator
	sub       <-chan roulettemp.PhaseMsg
	cancelSub func()

	snap   roulettemp.PhaseMsg
	wallet doors.Wallet
	now    time.Time

	// UI state.
	cursor    int
	chip      int32
	myBets    map[roulette.BetKey]int32 // resets each round
	lastErr   string
	showStats bool
	loading   bool

	// Animation state.
	ribbonScroll int
	ribbonFrame  int
	spinning     bool // local-animation flag; true while ribbon is decelerating
	winnerIdx    int  // RibbonOrder index of the winning pocket (-1 until known)
	pulse        components.PulseAnimation
	pulseActive  bool
	coins        components.CoinShower
	coinsActive  bool
	lastToken    int64 // phase token from the last applied snap (drift detection)
	lastPhase    roulettemp.Phase
}

func NewRoulette(sess *session.Session) tea.Model {
	return &Roulette{
		sess:      sess,
		chip:      5,
		cursor:    43, // start cursor on RED — a natural first-bet target
		myBets:    map[roulette.BetKey]int32{},
		winnerIdx: -1,
		loading:   true,
	}
}

// rouletteSnapMsg / rouletteWalletMsg / rouletteTickMsg / rouletteBetMsg are
// the model's internal message envelopes.
type rouletteSnapMsg struct{ snap roulettemp.PhaseMsg }
type rouletteWalletMsg struct {
	wallet doors.Wallet
	err    error
}
type rouletteTickMsg struct{}
type rouletteWallClockMsg struct{}
type rouletteBetMsg struct {
	err error
}

func (m *Roulette) Init() tea.Cmd {
	m.coord = m.sess.Roulette
	if m.coord == nil {
		m.lastErr = "roulette table unavailable"
		return nil
	}
	sub, cancel := m.coord.Subscribe()
	m.sub = sub
	m.cancelSub = cancel
	return tea.Batch(m.loadWallet(), m.waitSnap(), m.tickWallClock())
}

func (m *Roulette) loadWallet() tea.Cmd {
	user := m.sess.Identity.UserID
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3 * time.Second)
		defer cancel()
		w, err := svc.Load(ctx, user)
		return rouletteWalletMsg{wallet: w, err: err}
	}
}

func (m *Roulette) waitSnap() tea.Cmd {
	sub := m.sub
	return func() tea.Msg {
		snap, ok := <-sub
		if !ok {
			return nil
		}
		return rouletteSnapMsg{snap: snap}
	}
}

func (m *Roulette) tickWallClock() tea.Cmd {
	return tea.Tick(time.Duration(rouletteWallClockTickMs)*time.Millisecond, func(time.Time) tea.Msg {
		return rouletteWallClockMsg{}
	})
}

// scheduleSpinTick returns a tea.Cmd that fires after the right interval for
// the current frame index. Constant 50ms during the fast phase; eased up to
// ~200ms across the decel phase.
func (m *Roulette) scheduleSpinTick() tea.Cmd {
	var interval time.Duration
	if m.ribbonFrame < rouletteSpinFastFrames {
		interval = time.Duration(rouletteSpinFastIntervalMs) * time.Millisecond
	} else {
		step := m.ribbonFrame - rouletteSpinFastFrames
		interval = time.Duration(rouletteSpinFastIntervalMs+step*rouletteSpinDecelStepMs) * time.Millisecond
	}
	return tea.Tick(interval, func(time.Time) tea.Msg { return rouletteTickMsg{} })
}

func (m *Roulette) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case rouletteWalletMsg:
		m.loading = false
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		m.wallet = msg.wallet

	case rouletteSnapMsg:
		m.applySnap(msg.snap)
		return m, tea.Batch(m.waitSnap(), m.maybeAnimate())

	case rouletteTickMsg:
		if m.spinning {
			m.ribbonFrame++
			// Hold the visible scroll at the winning index once we hit the
			// total frame count — gives a moment of "the ball settles" before
			// the pulse animation starts.
			totalFrames := rouletteSpinFastFrames + rouletteSpinDecelFrames
			if m.ribbonFrame >= totalFrames {
				if m.winnerIdx >= 0 {
					m.ribbonScroll = m.winnerIdx
				}
				m.spinning = false
				// Kick off pulse + (optional) coin shower.
				m.startReveal()
				return m, m.activeAnimationsTick()
			}
			m.ribbonScroll++
			return m, m.scheduleSpinTick()
		}
		// During pulse/coin shower the tick is dispatched via
		// activeAnimationsTick which schedules its own follow-up.
		return m, nil

	case components.PulseTickMsg:
		if m.pulseActive {
			m.pulse.Step()
			if m.pulse.Done() {
				m.pulseActive = false
			}
		}
		return m, m.activeAnimationsTick()

	case components.CoinShowerTickMsg:
		if m.coinsActive {
			m.coins.Step()
			if m.coins.Done() {
				m.coinsActive = false
			}
		}
		return m, m.activeAnimationsTick()

	case rouletteWallClockMsg:
		m.now = time.Now()
		return m, m.tickWallClock()

	case rouletteBetMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
		} else {
			m.lastErr = ""
		}
		// Wallet refresh after a successful place so the BET line tracks the
		// debit. (Coordinator already pulled the credit; we re-fetch the
		// row.)
		return m, m.loadWallet()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// applySnap merges a fresh coordinator broadcast into the model. Detects
// phase transitions (via PhaseToken / Phase delta) and seeds the local
// animation state on Spinning entry; clears local myBets on Betting entry.
func (m *Roulette) applySnap(snap roulettemp.PhaseMsg) {
	prev := m.lastPhase
	m.snap = snap
	m.lastToken = snap.PhaseToken
	m.lastPhase = snap.Phase

	if snap.Phase == roulettemp.PhaseBetting && prev != roulettemp.PhaseBetting {
		// New round — clear our local "what did I bet" tally. Aggregate
		// comes from the snap itself.
		m.myBets = map[roulette.BetKey]int32{}
	}
	if snap.Phase == roulettemp.PhaseSpinning && snap.Winning != nil && !m.spinning {
		// Coordinator just drew the winner — seed the animation. Start
		// scrolling from a phase-shifted offset so it feels like the wheel
		// was already in motion.
		m.winnerIdx = roulette.RibbonIndex(*snap.Winning)
		m.spinning = true
		m.ribbonFrame = 0
		m.ribbonScroll = rand.Intn(len(roulette.RibbonOrder)) // start anywhere
	}
}

// maybeAnimate returns the spin-tick cmd if animation should be active.
// Lets applySnap kick off ticks without overlapping schedules with the
// existing rouletteTickMsg handler.
func (m *Roulette) maybeAnimate() tea.Cmd {
	if m.spinning && m.ribbonFrame == 0 {
		return m.scheduleSpinTick()
	}
	return nil
}

// activeAnimationsTick returns the next tick command for whichever
// animation(s) are running. Pulse and CoinShower can run in parallel; we
// schedule a single tick for the soonest deadline by deferring to one of
// them (they use independent intervals but both update via Step on every
// tick — overlap is benign).
func (m *Roulette) activeAnimationsTick() tea.Cmd {
	if m.pulseActive {
		return m.pulse.Tick()
	}
	if m.coinsActive {
		return m.coins.Tick()
	}
	return nil
}

// startReveal arms the pulse animation (always) and the coin shower (only
// when the local user actually won something this round). Called once the
// ribbon has finished its decel.
func (m *Roulette) startReveal() {
	if m.snap.Winning == nil {
		return
	}
	m.pulse = components.NewPulseAnimation(3)
	m.pulseActive = true
	// Did the local user win? Walk myBets against the winning pocket.
	var totalGross int32
	for key, amount := range m.myBets {
		gross := roulette.GrossReturn(*m.snap.Winning, roulette.Bet{Key: key, Amount: amount})
		totalGross += gross
	}
	if totalGross > 0 {
		// Reserve the same row count as slots so the body height stays
		// stable when the shower starts / stops.
		count := int(totalGross / 5)
		if count < 5 {
			count = 5
		}
		if count > 20 {
			count = 20
		}
		m.coins = components.NewCoinShower(rouletteCabinetWidth-2, count, time.Now().UnixNano())
		m.coinsActive = true
	}
}

func (m *Roulette) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.showStats {
		switch k.String() {
		case "esc", "s", "S":
			m.showStats = false
		}
		return m, nil
	}
	switch k.String() {
	case "esc":
		if m.cancelSub != nil {
			m.cancelSub()
		}
		return m, nav.Navigate(nav.DestDoors)
	case "left", "right", "up", "down":
		m.cursor = components.MoveRouletteCursor(m.cursor, k.String())
	case " ", "enter":
		return m, m.placeBetAtCursor()
	case "1", "2", "3", "4":
		// 1/2/3/4 swap the chip stack from the ladder.
		idx := int(k.String()[0] - '1')
		if idx >= 0 && idx < len(rouletteChipLadder) {
			m.chip = rouletteChipLadder[idx]
		}
	case "s", "S":
		m.showStats = true
	case "r", "R", "b", "B", "e", "E", "o", "O", "z", "Z":
		if cell := components.RouletteHotkeyCell(k.String()); cell >= 0 {
			m.cursor = cell
			return m, m.placeBetAtCursor()
		}
	case "L", "H":
		// Capital L / H = low / high (1-18 / 19-36). Lowercase l/h are
		// reserved by the carousel for ← / → so we use Shift to disambiguate.
		switch k.String() {
		case "L":
			m.cursor = 41
		case "H":
			m.cursor = 46
		}
		return m, m.placeBetAtCursor()
	}
	return m, nil
}

// placeBetAtCursor asynchronously asks the coordinator to record a bet for
// the currently-cursored cell with the active chip size. Bets are rejected
// outside the Betting window; the error surfaces via rouletteBetMsg.
func (m *Roulette) placeBetAtCursor() tea.Cmd {
	if m.coord == nil {
		m.lastErr = "table unavailable"
		return nil
	}
	if m.snap.Phase != roulettemp.PhaseBetting {
		m.lastErr = "no more bets — wait for next round"
		return nil
	}
	if m.chip <= 0 {
		return nil
	}
	if int64(m.chip) > m.wallet.Total() {
		m.lastErr = fmt.Sprintf("not enough credits — chip %d, you have %d", m.chip, m.wallet.Total())
		return nil
	}
	key := components.RouletteCellKey(m.cursor)
	bet := roulette.Bet{Key: key, Amount: m.chip}
	user := m.sess.Identity.UserID
	handle := m.sess.Identity.Handle
	coord := m.coord
	// Optimistically reflect the chip in the local view before the coord
	// confirms — keeps the felt responsive on slow PG. We undo on error.
	m.myBets[key] += m.chip
	return func() tea.Msg {
		err := coord.PlaceBet(user, handle, bet)
		return rouletteBetMsg{err: err}
	}
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

var (
	rouletteHint       = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	rouletteErr        = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	roulettePhaseLabel = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan)).Bold(true)
	rouletteWinLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorYellow)).Bold(true)
)

func (m *Roulette) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	if m.loading {
		return rouletteHint.Render("loading wallet…")
	}

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(" " + components.RenderRouletteHistory(m.snap.History) + "\n\n")
	b.WriteString(components.RenderRouletteRibbon(components.RouletteRibbonOpts{
		Scroll:     m.ribbonScroll,
		WinningIdx: m.winnerIdx,
		Locked:     !m.spinning && m.snap.Winning != nil,
		Spinning:   m.spinning,
	}))
	b.WriteString("\n\n")
	b.WriteString(components.RenderRouletteFelt(components.RouletteFeltOpts{
		Cursor:    m.cursor,
		MyBets:    m.myBets,
		Aggregate: m.snap.Aggregate,
		Width:     rouletteCabinetWidth - 2,
	}))
	b.WriteString("\n\n")
	b.WriteString(" " + m.renderStatusLine() + "\n")
	if m.lastErr != "" {
		b.WriteString(" " + rouletteErr.Render("! "+m.lastErr) + "\n")
	}

	bodyStr := b.String()

	// Coin shower overlays the top of the body during winnings reveal.
	if m.coinsActive {
		shower := m.coins.Render()
		bodyStr = shower + "\n" + bodyStr
	}

	frame := components.CabinetFrame(bodyStr, components.CabinetOpts{
		Title:      "Roulette",
		Width:      rouletteCabinetWidth,
		FeltAccent: theme.ColorFeltRoulette,
		Wallet: components.CabinetWallet{
			Bet:   m.chip,
			Total: m.wallet.Total(),
		},
		Footer: "←/→/↑/↓ cursor · Space bet · 1/2/3/4 chip · R/B/E/O/L/H quick · S stats · Esc back",
	})

	if m.showStats && m.coord != nil {
		stats := m.coord.ComputeStats()
		overlay := components.RenderRouletteStats(stats)
		dimmed := components.DimSGR(frame, theme.ColorDim)
		return components.Overlay(dimmed, overlay, m.sess.Width, m.sess.Height)
	}
	return frame
}

// renderStatusLine builds the one-row phase + countdown + cursor-label
// summary under the felt. Reads from m.snap so the same view code works
// during real time and during a frozen unit-test snapshot.
func (m *Roulette) renderStatusLine() string {
	phase := m.snap.Phase
	endsAt := m.snap.EndsAt
	now := m.now
	if now.IsZero() {
		now = time.Now()
	}
	remaining := endsAt.Sub(now)
	if remaining < 0 {
		remaining = 0
	}

	cursorKey := components.RouletteCellKey(m.cursor)
	cursorLabel := cursorKey.Type.String()
	if cursorKey.Type == roulette.BetStraight {
		cursorLabel = "straight " + cursorKey.Number.Number()
	}

	switch phase {
	case roulettemp.PhaseBetting:
		return roulettePhaseLabel.Render(fmt.Sprintf("BETTING — %ds left", int(remaining.Seconds()))) +
			"   " + rouletteHint.Render("cursor: "+cursorLabel)
	case roulettemp.PhaseNoMoreBets:
		return roulettePhaseLabel.Render("NO MORE BETS")
	case roulettemp.PhaseSpinning:
		return roulettePhaseLabel.Render("SPINNING…")
	case roulettemp.PhaseReveal:
		if m.snap.Winning != nil {
			p := *m.snap.Winning
			return rouletteWinLabel.Render(fmt.Sprintf("WINNER: %s %s",
				p.Number(), strings.ToUpper(p.Color().String())))
		}
		return roulettePhaseLabel.Render("REVEAL")
	}
	return ""
}
