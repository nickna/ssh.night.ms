// Package components — cardanim.go provides the tick-driven animations
// used by card games:
//
//   - DealAnimation: reveals cards one-at-a-time with a short delay between
//   - PulseAnimation: toggles bold/dim for a few cycles to draw the eye to
//     winning cards or a freshly-credited payout chip
//   - CoinShower: spawns '$' glyphs at random columns and floats them
//     upward over a short window — used in the wallet band on a win
//
// Each animation is a value type with a Step()/Tick()/Done() trio. The
// screen owns one as model state, dispatches its tick message in Update,
// and reflects its current state in View. No goroutines, no shared state
// — purely Bubble Tea–native.
package components

import (
	"math/rand"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// ---------------------------------------------------------------------------
// Deal animation
// ---------------------------------------------------------------------------

// DealTickMsg fires once per Deal interval. Only one Deal animation is
// expected to be active per screen, so an unnamed marker is enough.
type DealTickMsg struct{}

type DealAnimation struct {
	Revealed int
	Total    int
	Interval time.Duration
}

func NewDealAnimation(total int) DealAnimation {
	return DealAnimation{Revealed: 0, Total: total, Interval: 160 * time.Millisecond}
}

func (a DealAnimation) Done() bool { return a.Revealed >= a.Total }

// Tick returns the next-frame command, or nil when the animation has
// completed. Screens kick off the animation by returning a.Tick() and
// re-dispatching a.Tick() from Update on each DealTickMsg.
func (a DealAnimation) Tick() tea.Cmd {
	if a.Done() {
		return nil
	}
	return tea.Tick(a.Interval, func(time.Time) tea.Msg { return DealTickMsg{} })
}

func (a *DealAnimation) Step() {
	if a.Revealed < a.Total {
		a.Revealed++
	}
}

// ---------------------------------------------------------------------------
// Pulse (cycle bold/dim a few times)
// ---------------------------------------------------------------------------

type PulseTickMsg struct{}

type PulseAnimation struct {
	Phase     int           // current phase index
	MaxPhases int           // total phases to run before Done
	Interval  time.Duration // duration between phases
}

func NewPulseAnimation(cycles int) PulseAnimation {
	return PulseAnimation{Phase: 0, MaxPhases: cycles * 2, Interval: 220 * time.Millisecond}
}

func (a PulseAnimation) Done() bool { return a.Phase >= a.MaxPhases }

// Bright reports whether the pulse is currently in its "bright" half-cycle.
// Screens use this to alternate bold/normal styling.
func (a PulseAnimation) Bright() bool { return a.Phase%2 == 0 }

func (a PulseAnimation) Tick() tea.Cmd {
	if a.Done() {
		return nil
	}
	return tea.Tick(a.Interval, func(time.Time) tea.Msg { return PulseTickMsg{} })
}

func (a *PulseAnimation) Step() {
	if a.Phase < a.MaxPhases {
		a.Phase++
	}
}

// ---------------------------------------------------------------------------
// Coin shower (a brief upward spray of '$' glyphs)
// ---------------------------------------------------------------------------

type CoinShowerTickMsg struct{}

// CoinShowerHeight is the row count CoinShower.Render produces. Screens
// reserve this many rows in the layout so the rest of the body doesn't
// reflow when the shower starts/stops.
const CoinShowerHeight = 3

type coin struct {
	Col int
	Age int // 0 == bottom row, +1 each tick
}

type CoinShower struct {
	coins    []coin
	width    int
	ticks    int
	maxTicks int
	rng      *rand.Rand
	Interval time.Duration
}

// NewCoinShower spawns count coins at random columns across width cells.
// The shower runs for ~10 ticks (~0.8s at the default interval). seed
// lets tests pin the layout; pass time.Now().UnixNano() in production.
func NewCoinShower(width, count int, seed int64) CoinShower {
	r := rand.New(rand.NewSource(seed))
	cs := CoinShower{
		width:    width,
		maxTicks: 10,
		rng:      r,
		Interval: 90 * time.Millisecond,
	}
	for i := 0; i < count; i++ {
		col := r.Intn(width)
		// Stagger initial Y by a few rows so coins don't appear in a single
		// horizontal line on tick 0.
		cs.coins = append(cs.coins, coin{Col: col, Age: -r.Intn(3)})
	}
	return cs
}

func (a CoinShower) Done() bool { return a.ticks >= a.maxTicks }

func (a CoinShower) Tick() tea.Cmd {
	if a.Done() {
		return nil
	}
	return tea.Tick(a.Interval, func(time.Time) tea.Msg { return CoinShowerTickMsg{} })
}

func (a *CoinShower) Step() {
	a.ticks++
	for i := range a.coins {
		a.coins[i].Age++
	}
}

// Render returns a CoinShowerHeight-tall string with '$' glyphs at coin
// positions, age 0 on the bottom row rising to age (CoinShowerHeight-1)
// on the top. Older coins are dropped (they've flown off-screen). Each
// row is exactly width cells wide.
func (a CoinShower) Render() string {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCardHeld)).Bold(true)
	rows := make([][]rune, CoinShowerHeight)
	for i := range rows {
		rows[i] = []rune(strings.Repeat(" ", a.width))
	}
	for _, c := range a.coins {
		if c.Age < 0 || c.Age >= CoinShowerHeight {
			continue
		}
		row := CoinShowerHeight - 1 - c.Age
		if c.Col >= 0 && c.Col < a.width {
			rows[row][c.Col] = '$'
		}
	}
	out := make([]string, CoinShowerHeight)
	for i, r := range rows {
		out[i] = style.Render(string(r))
	}
	return strings.Join(out, "\n")
}

// RenderBlank returns an empty CoinShowerHeight-tall, width-wide block so
// screens can reserve the same vertical space whether or not a shower is
// currently running.
func RenderBlank(width, height int) string {
	row := strings.Repeat(" ", width)
	lines := make([]string, height)
	for i := range lines {
		lines[i] = row
	}
	return strings.Join(lines, "\n")
}
