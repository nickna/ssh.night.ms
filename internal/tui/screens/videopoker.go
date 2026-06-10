package screens

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/cards"
	"github.com/nickna/ssh.night.ms/internal/doors/videopoker"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// VideoPoker drives one hand of 9/6 Jacks or Better. Lifecycle:
//  1. load wallet
//  2. Deal pressed → bet debits, deal 5 cards
//  3. 1-5 toggles hold per card
//  4. Draw → replace non-held cards, evaluate, credit payout, log round
//  5. press Deal again to start the next hand
type VideoPoker struct {
	sess    *session.Session
	wallet  doors.Wallet
	bet     int32
	loading bool
	lastErr string

	game        *videopoker.Game
	finalRank   cards.HandRank
	finalPayout int32

	// dealStage reflects whether the in-flight deal animation is the
	// initial 5-card layout or the post-hold replacement deal. The reveal
	// budget differs (5 vs the count of non-held cards).
	dealStage vpDealStage
	deal      components.DealAnimation
	pulse     components.PulseAnimation
	shower    components.CoinShower
}

type vpDealStage int

const (
	vpDealNone    vpDealStage = iota
	vpDealInitial             // 5 cards going down in order
	vpDealReplace             // only the non-held cards refresh
)

func NewVideoPoker(sess *session.Session) tea.Model {
	return &VideoPoker{sess: sess, bet: 5, loading: true}
}

type vpWalletLoadedMsg struct {
	wallet doors.Wallet
	err    error
}

type vpDealtMsg struct {
	wallet doors.Wallet
	game   *videopoker.Game
	err    error
}

type vpDrawnMsg struct {
	wallet doors.Wallet
	rank   cards.HandRank
	payout int32
	err    error
}

func (m *VideoPoker) Init() tea.Cmd {
	user := m.sess.Identity.UserID
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3 * time.Second)
		defer cancel()
		w, err := svc.Load(ctx, user)
		return vpWalletLoadedMsg{wallet: w, err: err}
	}
}

func (m *VideoPoker) dealCmd() tea.Cmd {
	if m.wallet.Total() < int64(m.bet) {
		m.lastErr = fmt.Sprintf("not enough credits — bet %d, you have %d", m.bet, m.wallet.Total())
		return nil
	}
	bet := m.bet
	wallet := m.wallet
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3 * time.Second)
		defer cancel()
		if err := svc.Bet(ctx, &wallet, bet); err != nil {
			return vpDealtMsg{err: err}
		}
		g := videopoker.NewGame(doors.CryptoRng{})
		g.Deal()
		return vpDealtMsg{wallet: wallet, game: g}
	}
}

func (m *VideoPoker) drawCmd() tea.Cmd {
	if m.game == nil {
		return nil
	}
	bet := m.bet
	user := m.sess.Identity.UserID
	wallet := m.wallet
	game := m.game
	svc := m.sess.Wallet
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3 * time.Second)
		defer cancel()
		rank, payout := game.Draw(bet)
		if payout > 0 {
			if err := svc.Credit(ctx, &wallet, int64(payout)); err != nil {
				return vpDrawnMsg{wallet: wallet, rank: rank, payout: payout, err: err}
			}
		}
		hand := game.Hand()
		_ = svc.Record(ctx, doors.LedgerEntry{
			UserID: user, GameKey: "videopoker",
			Bet: bet, Payout: payout, Net: payout - bet,
			Details: map[string]any{
				"hand":   handToStrings(hand[:]),
				"rank":   rank.String(),
				"payout": payout,
			},
		})
		return vpDrawnMsg{wallet: wallet, rank: rank, payout: payout}
	}
}

func (m *VideoPoker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case vpWalletLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		m.wallet = msg.wallet

	case vpDealtMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		m.lastErr = ""
		m.wallet = msg.wallet
		m.game = msg.game
		m.finalRank = 0
		m.finalPayout = 0
		m.dealStage = vpDealInitial
		m.deal = components.NewDealAnimation(5)
		return m, m.deal.Tick()

	case vpDrawnMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
		}
		m.wallet = msg.wallet
		m.finalRank = msg.rank
		m.finalPayout = msg.payout
		// Animate just the replaced (non-held) cards. If the user held all
		// five, there's nothing to re-deal — skip straight to pulse/shower.
		replaced := 0
		held := m.game.Held()
		for _, h := range held {
			if !h {
				replaced++
			}
		}
		if replaced > 0 {
			m.dealStage = vpDealReplace
			m.deal = components.NewDealAnimation(replaced)
			cmds := []tea.Cmd{m.deal.Tick()}
			return m, tea.Batch(cmds...)
		}
		return m, m.winCmds()

	case components.DealTickMsg:
		m.deal.Step()
		if !m.deal.Done() {
			return m, m.deal.Tick()
		}
		// Replacement deal finished — kick off pulse + shower if the
		// final hand pays. Initial deal just ends here.
		if m.dealStage == vpDealReplace {
			m.dealStage = vpDealNone
			return m, m.winCmds()
		}
		m.dealStage = vpDealNone
		return m, nil

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
		case "left", "h":
			if m.game == nil {
				m.bet = stepBet(m.bet, -1)
			}
		case "right", "l":
			if m.game == nil {
				m.bet = stepBet(m.bet, +1)
			}
		case " ":
			// Block Space mid-animation so the user can't queue a draw
			// before the deal finishes laying out cards.
			if !m.deal.Done() {
				return m, nil
			}
			// Deal if no game or drawn already; Draw if hand is mid-flight.
			if m.game == nil {
				return m, m.dealCmd()
			}
			if dealt, drawn := m.game.State(); dealt && !drawn {
				return m, m.drawCmd()
			}
			// Already drawn — start a fresh hand.
			m.game = nil
			return m, m.dealCmd()
		case "1", "2", "3", "4", "5":
			if m.game == nil || !m.deal.Done() {
				return m, nil
			}
			if _, drawn := m.game.State(); drawn {
				return m, nil
			}
			idx := int(msg.String()[0] - '1')
			m.game.ToggleHold(idx)
		}
	}
	return m, nil
}

// Inline body styles. Card sprites + cabinet chrome live in
// internal/tui/components (cardart.go, cabinet.go).
var (
	vpHint     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	vpWin      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	vpLoss     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	vpHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccentDim))
	vpHoldTag  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorCardHeld))
	vpSlotTag  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	vpErrStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
)

const vpCabinetWidth = 60

// winCmds fires the pulse + coin shower if the final hand paid out,
// otherwise it's a no-op. Called from DealTickMsg when the replacement
// deal finishes (so the shower lines up with the user seeing the result).
func (m *VideoPoker) winCmds() tea.Cmd {
	if m.finalPayout <= 0 {
		return nil
	}
	m.pulse = components.NewPulseAnimation(3)
	m.shower = components.NewCoinShower(vpCabinetWidth-2, 10, time.Now().UnixNano())
	return tea.Batch(m.pulse.Tick(), m.shower.Tick())
}

// vpWinningIndices reports which of the 5 cards contributed to the
// classified hand rank. For 5-card hands (flush/straight/full house) all
// indices are returned; for n-of-a-kind and pair hands only the cards
// that participate. This is purely view-tier — it doesn't touch the
// videopoker package's evaluator.
func vpWinningIndices(hand [5]cards.Card, rank cards.HandRank) [5]bool {
	var marks [5]bool
	switch rank {
	case cards.RoyalFlush, cards.StraightFlush, cards.Flush, cards.Straight, cards.FullHouse:
		return [5]bool{true, true, true, true, true}
	case cards.FourOfAKind:
		markByRankCount(&marks, hand, 4, 0)
	case cards.ThreeOfAKind:
		markByRankCount(&marks, hand, 3, 0)
	case cards.TwoPair:
		markByRankCount(&marks, hand, 2, 0)
	case cards.JacksOrBetter:
		markByRankCount(&marks, hand, 2, int(cards.Jack))
	}
	return marks
}

// markByRankCount sets marks[i]=true for any card sharing a rank that
// appears `count` times in the hand. minRank filters to ranks at or
// above the given value (e.g. Jack for JacksOrBetter); pass 0 to allow
// all ranks.
func markByRankCount(marks *[5]bool, hand [5]cards.Card, count, minRank int) {
	counts := map[cards.Rank]int{}
	for _, c := range hand {
		counts[c.Rank]++
	}
	for i, c := range hand {
		if counts[c.Rank] == count && int(c.Rank) >= minRank {
			marks[i] = true
		}
	}
}

func (m *VideoPoker) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	var body strings.Builder
	if m.loading {
		body.WriteString("\n" + vpHint.Render("loading wallet…") + "\n")
	} else if m.game == nil {
		body.WriteString("\n\n" + vpHint.Render("press Space to deal") + "\n\n\n")
	} else {
		body.WriteString("\n" + m.renderHand())
		body.WriteString("\n")
		body.WriteString(m.renderHoldRow())
		body.WriteString("\n\n")
		body.WriteString(m.renderResultLine() + "\n\n")
		body.WriteString(m.renderPaytable())
	}
	body.WriteString("\n" + m.renderShowerBand())
	if m.lastErr != "" {
		body.WriteString("\n" + vpErrStyle.Render("! "+m.lastErr) + "\n")
	}

	return components.CabinetFrame(body.String(), components.CabinetOpts{
		Title:      "Video Poker — 9/6",
		Width:      vpCabinetWidth,
		FeltAccent: theme.ColorFeltVP,
		Wallet: components.CabinetWallet{
			Bet:    m.bet,
			Total:  m.wallet.Total(),
			Payout: m.payoutChip(),
		},
		Footer: "←/→ bet · Space deal/draw · 1-5 hold · Esc back",
	})
}

// renderHand lays out the five-card hand. The deal animation determines
// how many sprites are visible; held cards keep their position even
// during a replacement deal so the user can see what they kept.
func (m *VideoPoker) renderHand() string {
	hand := m.game.Hand()
	held := m.game.Held()
	_, drawn := m.game.State()

	// Decide which positions are currently visible. For the initial deal,
	// reveal left-to-right; for the replacement deal, reveal the non-held
	// slots in order while held cards stay visible the whole time.
	visible := vpVisibleMask(held, m.dealStage, m.deal)

	// Pulse override: winning cards alternate Winning/Normal while the
	// pulse animation runs.
	var winSet [5]bool
	if drawn && m.finalPayout > 0 {
		winSet = vpWinningIndices(hand, m.finalRank)
	}
	winState := components.CardStateWinning
	if !m.pulse.Done() && !m.pulse.Bright() {
		winState = components.CardStateNormal
	}

	sprites := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		if !visible[i] {
			sprites = append(sprites, components.RenderCardEmpty())
			continue
		}
		st := components.CardStateNormal
		switch {
		case winSet[i]:
			st = winState
		case held[i] && !drawn:
			st = components.CardStateHeld
		}
		sprites = append(sprites, components.RenderCard(hand[i], st))
	}
	return components.JoinCards(sprites...)
}

// renderHoldRow shows a numbered label under each card. Slots that are
// currently held display "HOLD" in gold; everything else shows the
// 1-based card index.
func (m *VideoPoker) renderHoldRow() string {
	held := m.game.Held()
	_, drawn := m.game.State()
	parts := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		var label string
		switch {
		case held[i] && !drawn:
			label = vpHoldTag.Render(" HOLD ")
		default:
			label = vpSlotTag.Render(fmt.Sprintf("  %d   ", i+1))
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, " ")
}

// vpVisibleMask returns which of the 5 card slots should currently
// render their face. Outside an active deal animation, every slot is
// visible. During an initial deal, slots 0..deal.Revealed-1 show.
// During a replacement deal, held slots are always visible and the
// non-held slots reveal in order as deal.Revealed advances.
func vpVisibleMask(held [5]bool, stage vpDealStage, deal components.DealAnimation) [5]bool {
	if deal.Done() {
		return [5]bool{true, true, true, true, true}
	}
	switch stage {
	case vpDealInitial:
		var v [5]bool
		for i := 0; i < 5 && i < deal.Revealed; i++ {
			v[i] = true
		}
		return v
	case vpDealReplace:
		v := held
		remaining := deal.Revealed
		for i := 0; i < 5 && remaining > 0; i++ {
			if !held[i] {
				v[i] = true
				remaining--
			}
		}
		return v
	}
	return [5]bool{true, true, true, true, true}
}

func (m *VideoPoker) renderResultLine() string {
	if m.game == nil {
		return ""
	}
	_, drawn := m.game.State()
	if !drawn {
		return vpHint.Render("hold any cards with 1-5, then Space to draw")
	}
	if m.finalPayout > 0 {
		return vpWin.Render(fmt.Sprintf("%s — +%d credits", m.finalRank.String(), m.finalPayout))
	}
	return vpLoss.Render("no win — Space for a new hand")
}

func (m *VideoPoker) renderPaytable() string {
	var b strings.Builder
	b.WriteString(vpHeader.Render("paytable (per credit bet)") + "\n")
	for _, row := range videopoker.Schedule() {
		b.WriteString(fmt.Sprintf("  %-18s %3d×\n", row.Name, row.Multiplier))
	}
	return b.String()
}

func (m *VideoPoker) renderShowerBand() string {
	if !m.shower.Done() {
		return m.shower.Render()
	}
	return components.RenderBlank(vpCabinetWidth-2, components.CoinShowerHeight)
}

// payoutChip returns the value to show as the gold "+N" chip in the
// cabinet's wallet row. Only set during a paying hand's settle window.
func (m *VideoPoker) payoutChip() int32 {
	if m.game == nil {
		return 0
	}
	if _, drawn := m.game.State(); !drawn {
		return 0
	}
	return m.finalPayout
}
