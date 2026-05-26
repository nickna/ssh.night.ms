// Package nav holds the navigation envelope shared between the root model,
// screens, and components. Lives outside `screens` so view components
// (carousel etc.) can reference Destination without importing the screen
// implementations that depend on them.
package nav

import tea "github.com/charmbracelet/bubbletea"

// Destination is the set of top-level lobby targets.
type Destination int

const (
	DestNone Destination = iota
	DestLobby
	DestChat
	DestBoards
	DestProfile
	DestNews
	DestGallery
	DestMap
	DestWeather
	DestAlerts
	DestFinance
	DestDoors
	DestSysop
	DestLogout

	// Door-game destinations. Reachable from the Doors carousel; each maps
	// 1:1 to a screen constructor in screens/*. Added when Doors moved from a
	// plain list to the shared carousel control.
	DestSlots
	DestVideoPoker
	DestBlackjack
	DestHoldem
	DestHoldemMP

	// DestLeaderboards lives next to the door games — it surfaces aggregated
	// game_rounds stats and is reached from the same Doors carousel.
	DestLeaderboards

	// DestWeb is the Carbonyl-backed full browser. Always shown in the
	// lobby; the screen itself surfaces the reason if a launch can't
	// actually happen (WS session, missing binary, kill switch off).
	DestWeb
)

// Title returns a short label suitable for the carousel and the placeholder
// screen header.
func (d Destination) Title() string {
	switch d {
	case DestLobby:
		return "Lobby"
	case DestChat:
		return "Chat"
	case DestBoards:
		return "Boards"
	case DestProfile:
		return "Profile"
	case DestNews:
		return "News"
	case DestGallery:
		return "Gallery"
	case DestMap:
		return "Map"
	case DestWeather:
		return "Weather"
	case DestAlerts:
		return "Alerts"
	case DestFinance:
		return "Finance"
	case DestDoors:
		return "Doors"
	case DestSysop:
		return "Sysop"
	case DestLogout:
		return "Logout"
	case DestSlots:
		return "Slots"
	case DestVideoPoker:
		return "Video Poker"
	case DestBlackjack:
		return "Blackjack"
	case DestHoldem:
		return "Hold'em"
	case DestHoldemMP:
		return "Hold'em MP"
	case DestLeaderboards:
		return "Leaderboards"
	case DestWeb:
		return "Web"
	}
	return ""
}

// NavigateMsg asks the root model to swap the active screen. Arg is an
// optional payload used by parameterized destinations (e.g. DestProfile with
// a handle string opens the Finger viewer on that user). Existing callers
// continue to pass only Target — Arg's zero value is the no-payload form.
type NavigateMsg struct {
	Target Destination
	Arg    string
}

// Navigate is a convenience cmd factory that the screens emit when the user
// picks a new destination.
func Navigate(target Destination) tea.Cmd {
	return func() tea.Msg { return NavigateMsg{Target: target} }
}

// NavigateWith is the parameterized variant. Used today by /finger to open
// the Profile screen on another user's handle.
func NavigateWith(target Destination, arg string) tea.Cmd {
	return func() tea.Msg { return NavigateMsg{Target: target, Arg: arg} }
}
