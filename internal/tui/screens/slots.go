package screens

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/slots"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Spin animation cadence — matches the .NET SlotsScreen timing so the
// "click click click" sequential reel-lock feels the same across both
// implementations. 60ms × 33 frames ≈ 2.0 s total spin.
const (
	slotsSpinFrameMs   = 60
	slotsReel1Lock     = 18
	slotsReel2Lock     = 24
	slotsReel3Lock     = 33
	slotsFlashFrameMs  = 80
	slotsFlashMaxFrame = 19
	// Multiplier threshold for the "jackpot" border-flash variant. 200×
	// covers Bar×3 (100×) … no, 250 = Seven×3. Anything ≥100 already feels
	// like a jackpot to the player so we keep the threshold generous.
	slotsJackpotMultiplier = 100
	// Outer chrome width (same as Blackjack so the wallet/footer rows align
	// between door games). The 38-col inner cabinet centers inside this.
	slotsCabinetWidth = 60
	slotsFeltColor    = "#7A2C8F" // muted Vegas-purple title tint
)

// Slots renders the slot-machine cabinet, drives the per-reel spin
// animation, and dispatches wallet operations through WalletService.
// View output composes the shared CabinetFrame chrome around the
// custom 38×13 SlotsCabinet body.
type Slots struct {
	sess    *session.Session
	wallet  doors.Wallet
	bet     int32
	loading bool
	lastErr string

	// Last settled spin — drives the outcome label + chip after the
	// animation finishes. Cleared at the start of each new spin.
	settled       *slots.Spin
	settledPayout int32

	// Per-frame animation state. cab* mirrors the SlotsCabinetState the
	// renderer takes — keeping the model fields close to the renderer
	// inputs avoids a separate translation step on every frame.
	spinning      bool
	spinFrame     int
	cabSpinning   [3]bool
	cabScroll     [3]int
	cabReels      [3]components.SlotSymbolID
	pendingSpin   slots.Spin
	pendingPayout int32
	pendingWallet doors.Wallet

	// Win-flash + coin-burst state. Set on a paying spin and ticked by
	// slotsFlashTickMsg until both the frame counter and the coin slice
	// drain to zero.
	winTier    components.SlotsWinTier
	flashFrame int
	coins      []slotsCoin
	coinRng    *rand.Rand

	// Paytable overlay toggle. While true, the body renders the paytable
	// modal on top of the dimmed cabinet and key input is constrained.
	showPaytable bool
}

// slotsCoin is the screen's internal coin state — coordinates are inside
// the slot cabinet (0..SlotsCabinetWidth-1, 0..SlotsCabinetHeight-1).
// framesAlive < 0 staggers the spawn so coins don't all rise in lockstep.
type slotsCoin struct {
	x, y        int
	framesAlive int
}

func NewSlots(sess *session.Session) tea.Model {
	return &Slots{sess: sess, bet: 5, loading: true}
}

type slotsWalletLoadedMsg struct {
	wallet doors.Wallet
	err    error
}

// slotsSpinReadyMsg fires once the bet has been debited and the payout
// (if any) credited. The View *only* starts the per-reel animation
// after this message — a failed ledger commit never paints a spin.
type slotsSpinReadyMsg struct {
	wallet doors.Wallet
	spin   slots.Spin
	payout int32
	err    error
}

type slotsSpinTickMsg struct{}
type slotsFlashTickMsg struct{}

func (m *Slots) Init() tea.Cmd {
	return m.loadWallet()
}

func (m *Slots) loadWallet() tea.Cmd {
	user := m.sess.Identity.UserID
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3*time.Second)
		defer cancel()
		w, err := svc.Load(ctx, user)
		return slotsWalletLoadedMsg{wallet: w, err: err}
	}
}

// spinCmd commits the bet (and payout, if any) to the wallet in one
// atomic-ish exchange before returning to the model. Animation only
// starts when slotsSpinReadyMsg arrives — i.e. after the ledger has
// accepted the round.
func (m *Slots) spinCmd() tea.Cmd {
	if m.wallet.Total() < int64(m.bet) {
		m.lastErr = fmt.Sprintf("not enough credits — bet %d, you have %d", m.bet, m.wallet.Total())
		return nil
	}
	bet := m.bet
	user := m.sess.Identity.UserID
	wallet := m.wallet
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3*time.Second)
		defer cancel()
		if err := svc.Bet(ctx, &wallet, bet); err != nil {
			return slotsSpinReadyMsg{err: err}
		}
		result := slots.Draw(doors.CryptoRng{})
		payout := slots.Payout(bet, result)
		if payout > 0 {
			if err := svc.Credit(ctx, &wallet, int64(payout)); err != nil {
				return slotsSpinReadyMsg{wallet: wallet, spin: result, payout: payout, err: err}
			}
		}
		_ = svc.Record(ctx, doors.LedgerEntry{
			UserID: user, GameKey: "slots",
			Bet: bet, Payout: payout, Net: payout - bet,
			Details: map[string]any{
				"reels":      []string{result.Reels[0].Name(), result.Reels[1].Name(), result.Reels[2].Name()},
				"multiplier": result.Multiplier,
			},
		})
		return slotsSpinReadyMsg{wallet: wallet, spin: result, payout: payout}
	}
}

func (m *Slots) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case slotsWalletLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		m.wallet = msg.wallet

	case slotsSpinReadyMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			m.spinning = false
			return m, nil
		}
		m.lastErr = ""
		m.pendingSpin = msg.spin
		m.pendingPayout = msg.payout
		m.pendingWallet = msg.wallet
		// Settled values are cleared so the outcome label doesn't read
		// from the previous round while the new one is animating.
		m.settled = nil
		m.settledPayout = 0
		// Start animation: all reels spinning, phase-shifted offsets so
		// the three strips don't move in visual lockstep.
		m.spinFrame = 0
		for i := 0; i < 3; i++ {
			m.cabSpinning[i] = true
			m.cabScroll[i] = i * 7
		}
		// Cancel any in-flight win flash from the previous spin.
		m.winTier = components.SlotsWinNone
		m.flashFrame = 0
		m.coins = nil
		return m, m.spinTick()

	case slotsSpinTickMsg:
		if !m.spinning {
			return m, nil
		}
		m.spinFrame++
		// Advance scroll on every still-spinning reel — produces the
		// scrolling-strip illusion until that reel locks below.
		for i := 0; i < 3; i++ {
			if m.cabSpinning[i] {
				m.cabScroll[i]++
			}
		}
		// Sequential reel locks. Frame numbers match the .NET cadence.
		switch m.spinFrame {
		case slotsReel1Lock:
			m.lockReel(0)
		case slotsReel2Lock:
			m.lockReel(1)
		}
		if m.spinFrame >= slotsReel3Lock {
			m.lockReel(2)
			return m, m.finishSpin()
		}
		return m, m.spinTick()

	case slotsFlashTickMsg:
		m.flashFrame++
		m.advanceCoins()
		if m.flashFrame >= slotsFlashMaxFrame && len(m.coins) == 0 {
			m.winTier = components.SlotsWinNone
			return m, nil
		}
		return m, m.flashTick()

	case tea.KeyMsg:
		// Paytable is a modal overlay — only Esc/P/Enter close it; other
		// keys are swallowed so the player can't bet-step while it's up.
		if m.showPaytable {
			switch msg.String() {
			case "esc", "p", "P", "enter", " ":
				m.showPaytable = false
			}
			return m, nil
		}
		// Hold any input during the spin animation so a held Enter can't
		// queue a second commit before the first round finishes.
		if m.spinning {
			return m, nil
		}
		switch msg.String() {
		case "esc":
			return m, nav.Navigate(nav.DestDoors)
		case " ", "enter":
			cmd := m.spinCmd()
			if cmd == nil {
				return m, nil
			}
			m.spinning = true
			return m, cmd
		case "left", "h":
			m.bet = stepBet(m.bet, -1)
		case "right", "l":
			m.bet = stepBet(m.bet, +1)
		case "p", "P":
			m.showPaytable = true
		}
	}
	return m, nil
}

// lockReel stops reel i's strip motion and pins the symbol from the
// pending spin result. Cast is safe because slots.Symbol and
// components.SlotSymbolID share the same ordering by design.
func (m *Slots) lockReel(i int) {
	m.cabSpinning[i] = false
	m.cabReels[i] = components.SlotSymbolID(m.pendingSpin.Reels[i])
}

// finishSpin runs when reel 3 has just locked. Promotes pending state to
// settled, releases the input lock, and kicks off the win flash + coin
// burst if the spin paid out.
func (m *Slots) finishSpin() tea.Cmd {
	spin := m.pendingSpin
	m.settled = &spin
	m.settledPayout = m.pendingPayout
	m.wallet = m.pendingWallet
	m.spinning = false
	if m.pendingPayout <= 0 {
		return nil
	}
	tier := components.SlotsWinNormal
	if m.pendingSpin.Multiplier >= slotsJackpotMultiplier {
		tier = components.SlotsWinJackpot
	}
	m.winTier = tier
	m.flashFrame = 0
	m.spawnCoins(m.pendingPayout / m.bet)
	return m.flashTick()
}

// spawnCoins seeds the coin burst with ~5–20 '$' glyphs scattered
// across the coin-tray row. Negative framesAlive staggers their first
// upward motion so the burst doesn't move in lockstep.
func (m *Slots) spawnCoins(multiplier int32) {
	if m.coinRng == nil {
		m.coinRng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	count := int(multiplier)
	if count < 5 {
		count = 5
	}
	if count > 20 {
		count = 20
	}
	m.coins = make([]slotsCoin, 0, count)
	for i := 0; i < count; i++ {
		m.coins = append(m.coins, slotsCoin{
			x:           2 + m.coinRng.Intn(components.SlotsCabinetWidth-4),
			y:           11,
			framesAlive: -m.coinRng.Intn(8),
		})
	}
}

func (m *Slots) advanceCoins() {
	if len(m.coins) == 0 {
		return
	}
	out := m.coins[:0]
	for _, c := range m.coins {
		c.framesAlive++
		// Move up every other frame so coins drift slow enough that the
		// player perceives individual glyphs rather than a streak.
		if c.framesAlive >= 0 && c.framesAlive%2 == 0 {
			c.y--
		}
		if c.y >= 3 {
			out = append(out, c)
		}
	}
	m.coins = out
}

func (m *Slots) spinTick() tea.Cmd {
	return tea.Tick(time.Duration(slotsSpinFrameMs)*time.Millisecond, func(time.Time) tea.Msg {
		return slotsSpinTickMsg{}
	})
}

func (m *Slots) flashTick() tea.Cmd {
	return tea.Tick(time.Duration(slotsFlashFrameMs)*time.Millisecond, func(time.Time) tea.Msg {
		return slotsFlashTickMsg{}
	})
}

// stepBet picks among a fixed ladder so a press doesn't change between
// "1 credit" and "1000 credits" — keeps the UI sensible.
func stepBet(cur int32, dir int) int32 {
	ladder := slotsBetLadder
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

var slotsBetLadder = [...]int32{1, 2, 5, 10, 25, 50, 100, 250, 500}

var (
	slotsHint    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	slotsWin     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	slotsLoss    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	slotsPipOn   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccent)).Bold(true)
	slotsPipOff  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	slotsErr     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	slotsLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	slotsSpinTxt = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan)).Italic(true)
)

func (m *Slots) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	var body strings.Builder
	if m.loading {
		body.WriteString("\n" + slotsHint.Render("loading wallet…") + "\n")
	} else {
		body.WriteString("\n")
		body.WriteString(m.renderCabinet())
		body.WriteString("\n\n")
		body.WriteString(m.renderBetLadder())
		body.WriteString("\n")
		body.WriteString(m.renderStatusLine())
		body.WriteString("\n")
	}
	if m.lastErr != "" {
		body.WriteString("\n" + slotsErr.Render("! "+m.lastErr))
	}

	frame := components.CabinetFrame(body.String(), components.CabinetOpts{
		Title:      "Slots",
		Width:      slotsCabinetWidth,
		FeltAccent: slotsFeltColor,
		Wallet: components.CabinetWallet{
			Bet:    m.bet,
			Total:  m.wallet.Total(),
			Payout: m.payoutChip(),
		},
		Footer: "←/→ bet · Space spin · P paytable · Esc back",
	})

	if m.showPaytable {
		return m.renderPaytableOverlay(frame)
	}
	return frame
}

// renderCabinet builds the cabinet state from the model's animation
// fields and centers the resulting 38-col block inside the chrome body.
func (m *Slots) renderCabinet() string {
	state := components.SlotsCabinetState{
		Spinning:     m.cabSpinning,
		ScrollOffset: m.cabScroll,
		WinTier:      m.winTier,
		FlashFrame:   m.flashFrame,
	}
	for i := 0; i < 3; i++ {
		if m.cabSpinning[i] {
			continue
		}
		state.Reels[i] = m.cabReels[i]
	}
	if m.settled == nil && !m.spinning {
		// Idle placeholder: show a static "Cherry / Lemon / Plum" trio so
		// the cabinet doesn't look broken on first paint.
		state.Reels = [3]components.SlotSymbolID{
			components.SlotCherry, components.SlotLemon, components.SlotPlum,
		}
	}
	for _, c := range m.coins {
		if c.framesAlive < 0 {
			continue
		}
		state.Coins = append(state.Coins, components.SlotsCoin{X: c.x, Y: c.y})
	}
	rendered := components.RenderSlotsCabinet(state)
	return indentBlock(rendered, slotsCabinetInnerPad())
}

// slotsCabinetInnerPad centers the 38-wide cabinet inside the cabinet
// chrome's inner body. indentBody (in cabinet.go) already adds one
// leading space, so the extra padding is the remainder.
func slotsCabinetInnerPad() int {
	innerWidth := slotsCabinetWidth - 2 // chrome gutter
	extra := innerWidth - components.SlotsCabinetWidth
	if extra < 0 {
		return 0
	}
	return extra / 2
}

func indentBlock(s string, pad int) string {
	if pad <= 0 {
		return s
	}
	prefix := strings.Repeat(" ", pad)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// renderBetLadder shows the nine bet steps as filled / hollow pips with
// the current credit value spelled out at the end. Visual at a glance
// is "how high up the ladder am I" without making the player count.
func (m *Slots) renderBetLadder() string {
	var b strings.Builder
	b.WriteString("  " + slotsLabel.Render("BET  "))
	curIdx := -1
	for i, v := range slotsBetLadder {
		if v == m.bet {
			curIdx = i
		}
	}
	for i := range slotsBetLadder {
		if i == curIdx {
			b.WriteString(slotsPipOn.Render("●"))
		} else {
			b.WriteString(slotsPipOff.Render("○"))
		}
		if i < len(slotsBetLadder)-1 {
			b.WriteString(" ")
		}
	}
	b.WriteString("   " + slotsLabel.Render(fmt.Sprintf("%d credits", m.bet)))
	return b.String()
}

// renderStatusLine is the one-row outcome / instruction text below the
// cabinet. While a spin is in flight, shows "spinning…"; otherwise shows
// the last settle's WIN / no-match line, or a press-to-spin nudge.
func (m *Slots) renderStatusLine() string {
	if m.spinning {
		return "  " + slotsSpinTxt.Render("spinning…")
	}
	if m.settled == nil {
		return "  " + slotsHint.Render("press Space to spin")
	}
	if m.settled.Winning {
		return "  " + slotsWin.Render(fmt.Sprintf("WIN! %d× → +%d credits", m.settled.Multiplier, m.settledPayout))
	}
	return "  " + slotsLoss.Render("no match — try again")
}

func (m *Slots) payoutChip() int32 {
	if m.settled == nil || !m.settled.Winning {
		return 0
	}
	return m.settledPayout
}

// renderPaytableOverlay composes the paytable modal on top of the dimmed
// cabinet view. The cabinet chrome stays readable behind it so the
// player keeps spatial context.
func (m *Slots) renderPaytableOverlay(base string) string {
	modal := slotsPaytableBox()
	w, h := m.sess.Width, m.sess.Height
	dimmed := components.DimSGR(base, theme.ColorDim)
	return components.Overlay(dimmed, modal, w, h)
}

// slotsPaytableBox renders the symbol → multiplier table as a bordered
// modal. Three-of-a-kind multipliers come straight from the slots
// package (mirrored here to avoid leaking those tables into the public
// API). Two-of-a-kind hints help the player read "near-miss" outcomes.
func slotsPaytableBox() string {
	style := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(lipgloss.Color(theme.ColorAccent)).
		Background(lipgloss.Color(theme.ColorSurface)).
		Foreground(lipgloss.Color(theme.ColorText)).
		Padding(1, 3)

	rows := []struct {
		Glyph string
		Mult  int
	}{
		{"7 7 7", 250},
		{"BAR BAR BAR", 100},
		{"BELL BELL BELL", 50},
		{"PLUM PLUM PLUM", 25},
		{"ORANGE×3", 15},
		{"LEMON×3", 10},
		{"CHERRY×3", 5},
	}

	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent)).Render("PAYTABLE"))
	b.WriteString("\n\n")
	b.WriteString(slotsLabel.Render("Three-of-a-kind:") + "\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("  %-18s  %4d×\n", r.Glyph, r.Mult))
	}
	b.WriteString("\n")
	b.WriteString(slotsLabel.Render("Two-of-a-kind:") + "\n")
	b.WriteString("  any two BAR or 7        5–10×\n")
	b.WriteString("  any two BELL or PLUM    3×\n")
	b.WriteString("  any two other matches   2×\n")
	b.WriteString("\n")
	b.WriteString(slotsHint.Render("press P / Esc / Space to close"))
	return style.Render(b.String())
}
