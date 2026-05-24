package screens

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/blackjack"
	"github.com/nickna/ssh.night.ms/internal/doors/cards"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Blackjack screen orchestrates one hand and rolls into the next on Space.
// Per-frame work: read m.game (or nil for "press space to deal"), render
// hands + outcome + wallet. The game logic is in internal/doors/blackjack —
// this is purely view.
type Blackjack struct {
	sess    *session.Session
	wallet  doors.Wallet
	bet     int32
	loading bool
	lastErr string

	game        *blackjack.Game
	settledRank string
	settledPay  int32

	// Animation state. Deal runs once per hand on the initial 4-card deal;
	// pulse + shower fire after a winning settle and decay over ~1s. All
	// three are inert (Done()) outside their active window.
	deal   components.DealAnimation
	pulse  components.PulseAnimation
	shower components.CoinShower
}

func NewBlackjack(sess *session.Session) tea.Model {
	return &Blackjack{sess: sess, bet: 5, loading: true}
}

type bjWalletMsg struct {
	wallet doors.Wallet
	err    error
}

type bjDealtMsg struct {
	wallet doors.Wallet
	game   *blackjack.Game
}

type bjBetErrMsg struct{ err error }

type bjSettledMsg struct {
	wallet  doors.Wallet
	outcome blackjack.Outcome
	payout  int32
	err     error
}

func (m *Blackjack) Init() tea.Cmd {
	user := m.sess.Identity.UserID
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3*time.Second)
		defer cancel()
		w, err := svc.Load(ctx, user)
		return bjWalletMsg{wallet: w, err: err}
	}
}

// dealCmd debits the starting bet and constructs a Game. Subsequent player
// actions (hit/stand/double) are local — only settlement re-enters the
// wallet path.
func (m *Blackjack) dealCmd() tea.Cmd {
	if m.wallet.Total() < int64(m.bet) {
		m.lastErr = fmt.Sprintf("not enough credits — bet %d, you have %d", m.bet, m.wallet.Total())
		return nil
	}
	bet := m.bet
	wallet := m.wallet
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3*time.Second)
		defer cancel()
		if err := svc.Bet(ctx, &wallet, bet); err != nil {
			return bjBetErrMsg{err: err}
		}
		return bjDealtMsg{wallet: wallet, game: blackjack.NewGame(doors.CryptoRng{})}
	}
}

// doubleCmd debits a second bet — game state already locked in by the time
// this fires.
func (m *Blackjack) doubleCmd() tea.Cmd {
	if !m.game.CanDouble() || m.wallet.Total() < int64(m.bet) {
		m.lastErr = "cannot double"
		return nil
	}
	bet := m.bet
	wallet := m.wallet
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3*time.Second)
		defer cancel()
		if err := svc.Bet(ctx, &wallet, bet); err != nil {
			return bjBetErrMsg{err: err}
		}
		return bjWalletMsg{wallet: wallet}
	}
}

// settleCmd credits the payout (if any), logs the round, returns the new
// wallet.
func (m *Blackjack) settleCmd() tea.Cmd {
	bet := m.bet
	user := m.sess.Identity.UserID
	wallet := m.wallet
	game := m.game
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3*time.Second)
		defer cancel()
		outcome := game.Outcome()
		payout := blackjack.Payout(bet, game.Doubled(), outcome)
		stake := bet
		if game.Doubled() {
			stake = bet * 2
		}
		if payout > 0 {
			if err := svc.Credit(ctx, &wallet, int64(payout)); err != nil {
				return bjSettledMsg{wallet: wallet, outcome: outcome, payout: payout, err: err}
			}
		}
		_ = svc.Record(ctx, doors.LedgerEntry{
			UserID: user, GameKey: "blackjack",
			Bet:    stake,
			Payout: payout,
			Net:    payout - stake,
			Details: map[string]any{
				"outcome": outcome.String(),
				"player":  handToStrings(game.Player()),
				"dealer":  handToStrings(game.Dealer()),
				"doubled": game.Doubled(),
			},
		})
		return bjSettledMsg{wallet: wallet, outcome: outcome, payout: payout}
	}
}

func handToStrings(h []cards.Card) []string {
	out := make([]string, len(h))
	for i, c := range h {
		out[i] = c.String()
	}
	return out
}

func (m *Blackjack) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case bjWalletMsg:
		m.loading = false
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		m.wallet = msg.wallet
	case bjDealtMsg:
		m.lastErr = ""
		m.wallet = msg.wallet
		m.game = msg.game
		m.settledRank = ""
		m.settledPay = 0
		// Animate the initial 4-card deal (P1, D1, P2, D2). Settle happens
		// once the deal completes — handled in DealTickMsg.
		m.deal = components.NewDealAnimation(4)
		return m, m.deal.Tick()
	case components.DealTickMsg:
		m.deal.Step()
		if !m.deal.Done() {
			return m, m.deal.Tick()
		}
		// Deal complete: if the hand is already over (immediate blackjack
		// or push), settle now.
		if m.game != nil && m.game.Finished() {
			return m, m.settleCmd()
		}
		return m, nil
	case bjBetErrMsg:
		m.lastErr = msg.err.Error()
	case bjSettledMsg:
		m.wallet = msg.wallet
		m.settledRank = msg.outcome.String()
		m.settledPay = msg.payout
		if m.isPlayerWin() {
			m.pulse = components.NewPulseAnimation(3)
			m.shower = components.NewCoinShower(cabinetBodyWidth(), 10, time.Now().UnixNano())
			return m, tea.Batch(m.pulse.Tick(), m.shower.Tick())
		}
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
			return m, nav.Navigate(nav.DestDoors)
		case "left":
			if m.game == nil || m.game.Finished() {
				m.bet = stepBet(m.bet, -1)
			}
		case "right":
			if m.game == nil || m.game.Finished() {
				m.bet = stepBet(m.bet, +1)
			}
		case " ":
			if m.game == nil || m.game.Finished() {
				m.game = nil
				return m, m.dealCmd()
			}
		case "h", "H":
			if m.game != nil && !m.game.Finished() && m.deal.Done() {
				m.game.Hit()
				if m.game.Finished() {
					return m, m.settleCmd()
				}
			}
		case "s", "S":
			if m.game != nil && !m.game.Finished() && m.deal.Done() {
				m.game.Stand()
				if m.game.Finished() {
					return m, m.settleCmd()
				}
			}
		case "d", "D":
			if m.game != nil && m.game.CanDouble() && m.deal.Done() {
				cmd := m.doubleCmd()
				m.game.Double()
				settle := m.settleCmd()
				if cmd == nil {
					return m, settle
				}
				return m, tea.Batch(cmd, settle)
			}
		}
	}
	return m, nil
}

// Inline body styles. Card sprites + cabinet chrome live in
// internal/tui/components (cardart.go, cabinet.go) so only the per-row
// labels and outcome text need styling here.
var (
	bjHint  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	bjWin   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	bjLoss  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	bjLabel = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	bjErr   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
)

func (m *Blackjack) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	// indentBody (in cabinet.go) prefixes every body line with the 1-col
	// gutter, so do NOT prepend manual leading spaces here — doing so only
	// indents the first line of multi-line sprites and breaks column
	// alignment.
	var body strings.Builder
	if m.loading {
		body.WriteString("\n" + bjHint.Render("loading wallet…") + "\n")
	} else if m.game == nil {
		body.WriteString("\n\n" + bjHint.Render("press Space to deal") + "\n\n\n")
	} else {
		dealerSt, playerSt := m.handStates()
		pVis, dVis := m.dealVisible()
		body.WriteString("\n" + bjLabel.Render(fmt.Sprintf("Dealer (%s)", dealerTotalString(m.game))) + "\n")
		body.WriteString(renderHand(m.game.Dealer(), m.game.HoleHidden(), dealerSt, dVis))
		body.WriteString("\n\n")
		body.WriteString(bjLabel.Render(fmt.Sprintf("Player (%d)", blackjack.HandValue(m.game.Player()))) + "\n")
		body.WriteString(renderHand(m.game.Player(), false, playerSt, pVis))
		body.WriteString("\n\n")
		body.WriteString(m.renderOutcome() + "\n")
	}
	// Reserve the coin-shower band so the wallet row never shifts when a
	// shower starts/stops.
	body.WriteString("\n" + m.renderShowerBand())
	if m.lastErr != "" {
		body.WriteString("\n" + bjErr.Render("! "+m.lastErr) + "\n")
	}

	return components.CabinetFrame(body.String(), components.CabinetOpts{
		Title:      "Blackjack",
		Width:      bjCabinetWidth,
		FeltAccent: theme.ColorFeltBJ,
		Wallet: components.CabinetWallet{
			Bet:    m.bet,
			Total:  m.wallet.Total(),
			Payout: m.payoutChip(),
		},
		Footer: "←/→ bet · Space deal · H hit · S stand · D double · Esc back",
	})
}

// renderShowerBand returns the 3-row coin-shower display when active,
// otherwise three blank rows of the same width so the body height stays
// constant.
func (m *Blackjack) renderShowerBand() string {
	if !m.shower.Done() {
		return m.shower.Render()
	}
	return components.RenderBlank(cabinetBodyWidth(), components.CoinShowerHeight)
}

// handStates picks the visual state for dealer and player hands based on
// the settled outcome. Pre-settle (or no game) everything stays Normal —
// we don't telegraph the result before settlement completes. When the
// pulse animation is active and currently in its dim half-cycle, the
// winning side temporarily drops back to Normal so the cards visibly
// flash.
func (m *Blackjack) handStates() (dealer, player components.CardState) {
	if m.game == nil || !m.game.Finished() || m.settledRank == "" {
		return components.CardStateNormal, components.CardStateNormal
	}
	winState := components.CardStateWinning
	if !m.pulse.Done() && !m.pulse.Bright() {
		winState = components.CardStateNormal
	}
	switch m.game.Outcome() {
	case blackjack.PlayerBlackjack, blackjack.PlayerWin, blackjack.DealerBust:
		return components.CardStateDimmed, winState
	case blackjack.DealerWin, blackjack.PlayerBust:
		return winState, components.CardStateDimmed
	default: // Push
		return components.CardStateNormal, components.CardStateNormal
	}
}

// isPlayerWin reports whether the most recent settled outcome paid the
// player anything (counts blackjack, regular win, and dealer bust; push
// returns the bet but doesn't earn a pulse).
func (m *Blackjack) isPlayerWin() bool {
	if m.game == nil || !m.game.Finished() {
		return false
	}
	switch m.game.Outcome() {
	case blackjack.PlayerBlackjack, blackjack.PlayerWin, blackjack.DealerBust:
		return true
	}
	return false
}

// cabinetBodyWidth returns the inner column count for the cabinet body —
// the cabinet draws a 1-col gutter on each side, so usable width is
// cabinetWidth-2. Kept as a function (not a const) so the cabinet
// width can be wired through later without spreading constants.
func cabinetBodyWidth() int { return bjCabinetWidth - 2 }

const bjCabinetWidth = 60

// payoutChip is the value to render as a gold "+N" chip in the wallet
// row. Only shown when the most recent settlement was a player win; reset
// to 0 each time a new hand begins.
func (m *Blackjack) payoutChip() int32 {
	if m.game == nil || !m.game.Finished() || m.settledRank == "" {
		return 0
	}
	switch m.game.Outcome() {
	case blackjack.PlayerBlackjack, blackjack.PlayerWin, blackjack.DealerBust:
		return m.settledPay
	}
	return 0
}

// dealerTotalString shows "?" while the hole card is hidden — otherwise
// renders the real value.
func dealerTotalString(g *blackjack.Game) string {
	if g.HoleHidden() && len(g.Dealer()) > 0 {
		// Show only the up-card's value.
		up := g.Dealer()[0]
		v, _ := upCardValue(up)
		return fmt.Sprintf("%d…", v)
	}
	return fmt.Sprintf("%d", blackjack.HandValue(g.Dealer()))
}

func upCardValue(c cards.Card) (int, bool) {
	switch c.Rank {
	case cards.Ace:
		return 11, true
	case cards.Jack, cards.Queen, cards.King:
		return 10, false
	default:
		return int(c.Rank), false
	}
}

// renderHand lays out a hand as side-by-side card sprites. hideSecond
// renders the dealer's hole card as a face-down back. st applies the same
// state to every visible (face-up) card; the hole card always renders
// as a plain back regardless of state. limit caps the number of visible
// cards (used by the deal animation to reveal one at a time); -1 means
// no limit.
func renderHand(h []cards.Card, hideSecond bool, st components.CardState, limit int) string {
	if limit < 0 || limit > len(h) {
		limit = len(h)
	}
	if limit == 0 {
		return ""
	}
	sprites := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		if i == 1 && hideSecond {
			sprites = append(sprites, components.RenderCardBack(components.CardStateNormal))
		} else {
			sprites = append(sprites, components.RenderCard(h[i], st))
		}
	}
	return components.JoinCards(sprites...)
}

// dealVisible returns how many cards of each hand should currently be
// visible given the deal animation's progress. The deal order is
// alternating P1, D1, P2, D2 (Revealed counts 0..4). Cards added later
// via Hit are always rendered (those happen after the initial deal
// finishes).
func (m *Blackjack) dealVisible() (player, dealer int) {
	if m.deal.Done() {
		// Animation finished — show everything in both hands.
		if m.game == nil {
			return 0, 0
		}
		return len(m.game.Player()), len(m.game.Dealer())
	}
	r := m.deal.Revealed
	// Player gets cards at deal steps 1 and 3; dealer at 2 and 4.
	switch {
	case r >= 4:
		return 2, 2
	case r == 3:
		return 2, 1
	case r == 2:
		return 1, 1
	case r == 1:
		return 1, 0
	}
	return 0, 0
}

func (m *Blackjack) renderOutcome() string {
	if m.game == nil || !m.game.Finished() {
		return ""
	}
	if m.settledRank == "" {
		// Settle hasn't completed yet — keep the view stable.
		return ""
	}
	switch m.game.Outcome() {
	case blackjack.PlayerBlackjack, blackjack.PlayerWin, blackjack.DealerBust:
		return bjWin.Render(fmt.Sprintf("%s — +%d credits", m.settledRank, m.settledPay))
	case blackjack.Push:
		return bjHint.Render(fmt.Sprintf("%s — bet returned (+%d)", m.settledRank, m.settledPay))
	default:
		return bjLoss.Render(m.settledRank)
	}
}
