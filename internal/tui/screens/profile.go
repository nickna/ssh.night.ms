package screens

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/providers/geocoding"
	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Profile is the TUI Profile destination — the .NET ProfileEditScreen +
// PasswordChangeScreen + KeysManagementScreen + FingerScreen rolled into one
// tea.Model with an internal mode state machine (mirrors the Boards pattern).
//
// modeFinger renders someone else's profile when the constructor variant
// NewProfileFinger is used (e.g., /finger @handle in chat). The other modes
// operate on the logged-in user's row loaded via ProfileService.GetByID.
type Profile struct {
	sess *session.Session

	mode       profileMode
	focusIndex int

	// snap is the loaded user row. nil before profileLoadedMsg lands.
	snap *realtime.ProfileSnapshot
	keys []gen.IdentityCredential

	// viewingHandle is non-empty when constructed via NewProfileFinger — the
	// screen renders modeFinger immediately and Esc returns to the lobby.
	viewingHandle string

	// Profile tab inputs. Initialized from snap on load.
	realName textinput.Model
	location textinput.Model
	bio      textarea.Model
	tz       *components.SearchableList
	tempUnit components.OptionSelector
	clock    components.OptionSelector
	dateFmt  components.OptionSelector

	// Settings tab inputs.
	suppressKeys components.Checkbox
	requireSsh   components.Checkbox

	// Locations modal state. savedLocations is the cached list; locCursor is
	// the row focus for delete. locAddOpen toggles the inline 3-input add
	// form (label / lat / lon); locFormFocus picks which input owns the
	// cursor. locRenameOpen replaces the add form with a single-input
	// rename form when 'r' is pressed; locRenameID is the target row.
	// locSearchResults / locSearching back the Ctrl+F geocoder lookup —
	// non-empty results are rendered as a 1-N numbered picker above the
	// add form. locErr surfaces validation/back-end errors above the form.
	savedLocations    []realtime.SavedLocation
	locCursor         int
	locAddOpen        bool
	locRenameOpen     bool
	locRenameID       int64
	locFormLabel      textinput.Model
	locFormLat        textinput.Model
	locFormLon        textinput.Model
	locFormFocus      int
	// locFormCanonical is set by the geocoder picker when the user picks
	// a result — preserves the disambiguating "Name, Admin1, Country"
	// string so it lands in user_saved_locations.canonical even after the
	// user edits the label to something shorter ("Office"). Cleared on
	// form reset and on manual edits of the label.
	locFormCanonical  string
	locSearchResults  []geocoding.Result
	locSearching      bool
	locErr            string

	// Password modal inputs.
	pwCurrent    textinput.Model
	pwNew        textinput.Model
	pwConfirm    textinput.Model
	pwFocusIndex int
	pwErr        string

	// Keys modal cursor.
	keysCursor int

	// Add-key modal inputs. Built fresh in openAddKey so a cancelled attempt
	// leaves no residue. addKeyFocus: 0=public-key, 1=label.
	addKeyPublic textinput.Model
	addKeyLabel  textinput.Model
	addKeyFocus  int
	addKeyErr    string
	addKeyBusy   bool

	// Confirm-overlay state. confirmKind identifies which guard fired so
	// the Yes-branch can dispatch the right follow-up cmd. confirmReturnMode
	// is the mode to restore when the overlay closes — kept separate from
	// previousMode so a confirm raised on top of e.g. modeKeys doesn't
	// clobber the outer parent (modeTabProfile) and strand Esc.
	confirm           *components.Confirm
	confirmKind       string
	confirmReturnMode profileMode

	// Cross-mode UI state.
	working    bool
	notice     string
	noticeKind string // "" / "ok" / "err"

	// previousMode tracks where to return when a screen-level modal
	// (modeKeys / modePassword / modeLocations) is dismissed. Sub-overlays
	// (modeAddKey, modeConfirm) must NOT write here — their return targets
	// are tracked separately so nested escapes bubble out correctly.
	previousMode profileMode
}

// profileMode is the internal mini-router. Tabs are top-level views;
// modePassword / modeKeys / modeConfirm overlay one of them. modeFinger is
// the dedicated read-only view used when looking at someone else's profile.
type profileMode int

const (
	modeTabProfile profileMode = iota
	modeTabSettings
	modePassword
	modeKeys
	modeAddKey
	modeLocations
	modeConfirm
	modeFinger
)

// NewProfile builds the Profile screen pointed at the logged-in user.
func NewProfile(sess *session.Session) tea.Model {
	return &Profile{sess: sess, mode: modeTabProfile}
}

// NewProfileFinger builds the Profile screen pointed at someone else's
// handle in read-only Finger mode. Esc returns to the lobby.
func NewProfileFinger(sess *session.Session, handle string) tea.Model {
	return &Profile{sess: sess, mode: modeFinger, viewingHandle: strings.TrimPrefix(handle, "@")}
}

//
// Msg envelopes
//

type profileLoadedMsg struct {
	snap *realtime.ProfileSnapshot
	keys []gen.IdentityCredential
	err  error
}

type profileSavedMsg struct{ err error }
type passwordChangedMsg struct{ err error }
type keysReloadedMsg struct {
	keys []gen.IdentityCredential
	err  error
}
type keyRemovedMsg struct {
	id  int64
	err error
}
type keyAddedMsg struct {
	err error
	// fingerprint is captured pre-insert so the success branch can audit-log
	// without a re-read.
	fingerprint string
}
type profileErrMsg struct {
	stage string
	err   error
}

//
// Lifecycle
//

func (m *Profile) Init() tea.Cmd {
	switch m.mode {
	case modeFinger:
		return m.loadFinger(m.viewingHandle)
	default:
		return m.loadSelf()
	}
}

// loadSelf fetches the logged-in user's snapshot + SSH key list. Both land
// in profileLoadedMsg so Update can populate the editable widgets in one
// shot.
func (m *Profile) loadSelf() tea.Cmd {
	svc := m.sess.Profile
	queries := m.sess.Queries
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		snap, err := svc.GetByID(ctx, userID)
		if err != nil {
			return profileLoadedMsg{err: err}
		}
		keys, err := queries.ListSshCredentialsForUser(ctx, userID)
		if err != nil {
			return profileLoadedMsg{snap: snap, err: err}
		}
		return profileLoadedMsg{snap: snap, keys: keys}
	}
}

// loadFinger fetches another user's snapshot for the read-only viewer.
// The viewer's display prefs come from the Session-cached DisplayPrefs
// (loaded at session-attach), so no extra round trip is needed here.
// The keys list isn't loaded — Finger mode never shows them.
func (m *Profile) loadFinger(handle string) tea.Cmd {
	svc := m.sess.Profile
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		snap, err := svc.GetByHandle(ctx, handle)
		if err != nil {
			return profileLoadedMsg{err: err}
		}
		return profileLoadedMsg{snap: snap}
	}
}

// hydrate populates the editable widgets from the loaded snapshot. Idempotent
// so re-loads after a save replay cleanly.
func (m *Profile) hydrate() {
	if m.snap == nil {
		return
	}
	rn := textinput.New()
	rn.CharLimit = realtime.MaxRealNameLength
	rn.Width = 40
	rn.Placeholder = "(optional)"
	rn.SetValue(m.snap.RealName)
	m.realName = rn

	loc := textinput.New()
	loc.CharLimit = realtime.MaxLocationLength
	loc.Width = 40
	loc.Placeholder = "(optional)"
	loc.SetValue(m.snap.Location)
	m.location = loc

	bio := textarea.New()
	bio.CharLimit = realtime.MaxBioLength
	bio.SetWidth(40)
	bio.SetHeight(4)
	bio.ShowLineNumbers = false
	bio.Placeholder = "Tell other users about yourself…"
	bio.SetValue(m.snap.Bio)
	m.bio = bio

	tz := m.snap.TimeZoneID
	if tz == "" {
		tz = "UTC"
	}
	m.tz = components.NewSearchableList(IANAZones, tz)
	m.tz.Width = 36

	m.tempUnit = components.OptionSelector{Options: []string{"Celsius", "Fahrenheit", "Both"}, Index: clampIndex(int(m.snap.TemperatureUnit), 3)}
	m.clock = components.OptionSelector{Options: []string{"24-hour", "12-hour"}, Index: clampIndex(int(m.snap.ClockFormat), 2)}
	m.dateFmt = components.OptionSelector{Options: []string{"YYYY-MM-DD", "M/D/YYYY", "D/M/YYYY"}, Index: clampIndex(int(m.snap.DateFormat), 3)}

	m.suppressKeys = components.Checkbox{Label: "suppress key-adoption prompts", Checked: m.snap.SuppressKeyAdoptionPrompts}
	m.requireSsh = components.Checkbox{Label: "require SSH key (passwordless login)", Checked: m.snap.RequireSshKey}

	m.applyFocus()
}

func clampIndex(v, max int) int {
	if v < 0 || v >= max {
		return 0
	}
	return v
}

//
// Update
//

func (m *Profile) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case profileLoadedMsg:
		if msg.err != nil {
			m.notice = "load failed: " + msg.err.Error()
			m.noticeKind = "err"
			return m, nil
		}
		m.snap = msg.snap
		m.keys = msg.keys
		if m.mode != modeFinger {
			m.hydrate()
		}
		return m, nil

	case profileSavedMsg:
		m.working = false
		if msg.err != nil {
			m.notice = "save failed: " + msg.err.Error()
			m.noticeKind = "err"
			return m, nil
		}
		m.notice = "saved."
		m.noticeKind = "ok"
		// Refresh the cached display prefs so the status bar + any
		// subsequent /finger render pick up the new clock / zone / date
		// format on this same session, no re-login required.
		refreshCtx, cancel := m.sess.CtxWithTimeout(2*time.Second)
		if err := m.sess.RefreshDisplayPrefs(refreshCtx); err != nil {
			m.sess.Logger.Warn("refresh display prefs", "err", err)
		}
		cancel()
		// Re-load so derived fields (HasPassword, counts) refresh.
		return m, m.loadSelf()

	case passwordChangedMsg:
		m.working = false
		if msg.err != nil {
			m.pwErr = msg.err.Error()
			return m, nil
		}
		m.mode = modeTabProfile
		m.pwErr = ""
		m.pwCurrent.SetValue("")
		m.pwNew.SetValue("")
		m.pwConfirm.SetValue("")
		m.notice = "password updated."
		m.noticeKind = "ok"
		return m, m.loadSelf()

	case keysReloadedMsg:
		m.working = false
		if msg.err != nil {
			m.notice = "keys reload failed: " + msg.err.Error()
			m.noticeKind = "err"
			return m, nil
		}
		m.keys = msg.keys
		if m.keysCursor >= len(m.keys) {
			m.keysCursor = 0
		}
		return m, nil

	case keyRemovedMsg:
		m.working = false
		if msg.err != nil {
			m.notice = "remove failed: " + msg.err.Error()
			m.noticeKind = "err"
			return m, nil
		}
		m.notice = "key removed."
		m.noticeKind = "ok"
		return m, m.reloadKeys()

	case keyAddedMsg:
		m.addKeyBusy = false
		if msg.err != nil {
			m.addKeyErr = msg.err.Error()
			return m, nil
		}
		m.mode = modeKeys
		m.addKeyErr = ""
		m.addKeyPublic.SetValue("")
		m.addKeyLabel.SetValue("")
		m.notice = "ssh key added."
		m.noticeKind = "ok"
		return m, m.reloadKeys()

	case profileErrMsg:
		m.notice = msg.stage + ": " + msg.err.Error()
		m.noticeKind = "err"
		return m, nil

	case locationsLoadedMsg:
		if msg.err != nil {
			m.locErr = msg.err.Error()
			return m, nil
		}
		m.savedLocations = msg.locs
		if m.locCursor >= len(m.savedLocations) {
			m.locCursor = 0
		}
		return m, nil

	case locationSearchMsg:
		m.locSearching = false
		if msg.err != nil {
			m.locErr = "search failed: " + msg.err.Error()
			m.locSearchResults = nil
			return m, nil
		}
		if len(msg.results) == 0 {
			m.locErr = "no matches — try a different name."
			m.locSearchResults = nil
			return m, nil
		}
		m.locErr = ""
		m.locSearchResults = msg.results
		return m, nil

	case locationMutatedMsg:
		m.working = false
		if msg.err != nil {
			switch {
			case errors.Is(msg.err, realtime.ErrLocationDuplicateLabel):
				m.locErr = "that label is already in use."
			case errors.Is(msg.err, realtime.ErrLocationLimitReached):
				m.locErr = fmt.Sprintf("limit reached — %d saved locations max.", realtime.MaxSavedLocationsPerUser)
			default:
				m.locErr = msg.err.Error()
			}
			return m, nil
		}
		m.locErr = ""
		m.locAddOpen = false
		m.locRenameOpen = false
		m.locRenameID = 0
		// Refresh the Session's PrimaryLocation cache so WeatherCoords()
		// picks up the new state, then reload the list for the modal.
		refreshCtx, cancel := m.sess.CtxWithTimeout(2*time.Second)
		if err := m.sess.RefreshPrimaryLocation(refreshCtx); err != nil {
			m.sess.Logger.Warn("refresh primary location", "err", err)
		}
		cancel()
		return m, m.reloadLocations()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Profile) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeTabProfile:
		return m.handleProfileTabKey(k)
	case modeTabSettings:
		return m.handleSettingsTabKey(k)
	case modePassword:
		return m.handlePasswordKey(k)
	case modeKeys:
		return m.handleKeysKey(k)
	case modeAddKey:
		return m.handleAddKeyKey(k)
	case modeLocations:
		return m.handleLocationsKey(k)
	case modeConfirm:
		return m.handleConfirmKey(k)
	case modeFinger:
		return m.handleFingerKey(k)
	}
	return m, nil
}

// profileTabStops is the number of focusable widgets on the profile tab.
// Order: realName, location, bio, tz, tempUnit, clock, dateFmt,
// SaveBtn, PasswordBtn, KeysBtn, ViewSelfBtn, LocationsBtn.
const profileTabStops = 12

// settingsTabStops: suppressKeys, requireSsh, SaveBtn.
const settingsTabStops = 3

// Module-level cached styles. Each lipgloss.Style construction allocates;
// holding them as package vars means renderTabStrip / renderProfileLeft etc.
// don't churn the GC at 60 FPS when the user has an animation pending in
// another part of the UI. .Render(string) on a cached Style is allocation-
// free for the style itself; the formatted bytes are unavoidable.
var (
	profileTabActiveStyle = lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color(theme.ColorAccent)).
				Background(lipgloss.Color(theme.ColorSurfaceAlt)).Padding(0, 2)
	profileTabIdleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.ColorAccentDim)).Padding(0, 2)
	profileTabRightStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.ColorDim))

	profileHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.ColorDim)).Italic(true)
	profileNoticeBase  = lipgloss.NewStyle().Bold(true)
	profileNoticeOk    = profileNoticeBase.Foreground(lipgloss.Color(theme.ColorGreen))
	profileNoticeErr   = profileNoticeBase.Foreground(lipgloss.Color(theme.ColorRed))
	profileNoticeDim   = profileNoticeBase.Foreground(lipgloss.Color(theme.ColorAccentDim))

	profileSysopBadgeStyle = lipgloss.NewStyle().Bold(true).
				Background(lipgloss.Color(theme.ColorYellow)).
				Foreground(lipgloss.Color(theme.ColorBackground)).
				Padding(0, 1)
	profileIdentityStyle = lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color(theme.ColorAccent))
	profileMutedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.ColorAccentDim))
	profileLabelStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	profileActiveLabelStyle = lipgloss.NewStyle().Bold(true).
					Foreground(lipgloss.Color(theme.ColorAccent))

	profileButtonFocused = lipgloss.NewStyle().Bold(true).
				Background(lipgloss.Color(theme.ColorSurfaceAlt)).
				Foreground(lipgloss.Color(theme.ColorYellow)).
				Padding(0, 1)
	profileButtonIdle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.ColorAccentDim)).
				Padding(0, 1)
)

// handleProfileTabKey routes keys for the profile tab. Global keys (Esc /
// Tab / Ctrl+S / Ctrl+T) intercept before the focused widget's own handler.
func (m *Profile) handleProfileTabKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Searchable-list eats most printable keys when focused, but the parent
	// still owns Tab/Shift-Tab/Esc/Ctrl+S so the user can leave the field.
	if m.focusIndex == 3 && m.snap != nil {
		switch k.String() {
		case "tab", "shift+tab", "esc", "ctrl+s", "ctrl+t", "enter":
			// Fall through to global handler below.
		default:
			if m.tz.Update(k) {
				return m, nil
			}
		}
	}

	switch k.String() {
	case "esc":
		return m, nav.Navigate(nav.DestLobby)
	case "ctrl+t":
		m.mode = modeTabSettings
		m.focusIndex = 0
		m.applyFocus()
		return m, nil
	case "ctrl+s":
		return m, m.submitProfile()
	case "tab":
		m.focusIndex = (m.focusIndex + 1) % profileTabStops
		m.applyFocus()
		return m, textinput.Blink
	case "shift+tab":
		m.focusIndex = (m.focusIndex - 1 + profileTabStops) % profileTabStops
		m.applyFocus()
		return m, textinput.Blink
	case "enter":
		// Buttons fire on Enter; option-selectors and lists do nothing here.
		switch m.focusIndex {
		case 3:
			// Enter on tz commits the choice; advance focus.
			m.focusIndex = 4
			m.applyFocus()
			return m, nil
		case 7:
			return m, m.submitProfile()
		case 8:
			return m, m.openPassword()
		case 9:
			return m, m.openKeys()
		case 10:
			// View as others see me: open Finger on our own handle.
			return m, nav.NavigateWith(nav.DestProfile, m.sess.Identity.Handle)
		case 11:
			return m, m.openLocations()
		}
	case "left":
		switch m.focusIndex {
		case 4:
			m.tempUnit.Prev()
		case 5:
			m.clock.Prev()
		case 6:
			m.dateFmt.Prev()
		}
		return m, nil
	case "right":
		switch m.focusIndex {
		case 4:
			m.tempUnit.Next()
		case 5:
			m.clock.Next()
		case 6:
			m.dateFmt.Next()
		}
		return m, nil
	}

	// Forward typing to whichever input owns the cursor.
	var cmd tea.Cmd
	switch m.focusIndex {
	case 0:
		m.realName, cmd = m.realName.Update(k)
	case 1:
		m.location, cmd = m.location.Update(k)
	case 2:
		m.bio, cmd = m.bio.Update(k)
	}
	return m, cmd
}

// applyFocus pushes the focus state into each widget so visual cues + cursor
// blink match the focus index. Called whenever focusIndex changes.
func (m *Profile) applyFocus() {
	if m.snap == nil {
		return
	}
	switch m.mode {
	case modeTabProfile:
		m.realName.Blur()
		m.location.Blur()
		m.bio.Blur()
		m.tz.Focus = false
		m.tempUnit.Focus = false
		m.clock.Focus = false
		m.dateFmt.Focus = false
		switch m.focusIndex {
		case 0:
			m.realName.Focus()
		case 1:
			m.location.Focus()
		case 2:
			m.bio.Focus()
		case 3:
			m.tz.Focus = true
		case 4:
			m.tempUnit.Focus = true
		case 5:
			m.clock.Focus = true
		case 6:
			m.dateFmt.Focus = true
		}
	case modeTabSettings:
		m.suppressKeys.Focus = m.focusIndex == 0
		m.requireSsh.Focus = m.focusIndex == 1
	}
}

// submitProfile writes the current widget state through ProfileService.
// Pulls fields from BOTH tabs since the model carries both unchanged-when-not-
// edited (snap holds the original, but we mirror current widget state into
// the ProfileUpdate either way).
func (m *Profile) submitProfile() tea.Cmd {
	if m.snap == nil {
		return nil
	}
	if m.working {
		return nil
	}
	m.working = true
	m.notice = "saving…"
	m.noticeKind = ""

	update := realtime.ProfileUpdate{
		RealName:                   strings.TrimSpace(m.realName.Value()),
		Location:                   strings.TrimSpace(m.location.Value()),
		Bio:                        strings.TrimSpace(m.bio.Value()),
		TimeZoneID:                 m.tz.Selected(),
		TemperatureUnit:            int32(m.tempUnit.Index),
		ClockFormat:                int32(m.clock.Index),
		DateFormat:                 int32(m.dateFmt.Index),
		SuppressKeyAdoptionPrompts: m.suppressKeys.Checked,
		RequireSshKey:              m.requireSsh.Checked,
	}
	if update.TimeZoneID == "" {
		update.TimeZoneID = "UTC"
	}
	svc := m.sess.Profile
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		return profileSavedMsg{err: svc.UpdateProfile(ctx, userID, update)}
	}
}

//
// Keys modal
//

//
// Confirm modal
//

func (m *Profile) handleConfirmKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirm == nil {
		m.mode = m.confirmReturnMode
		return m, nil
	}
	m.confirm.Update(k)
	if m.confirm.Cancelled {
		m.confirm = nil
		m.confirmKind = ""
		m.mode = m.confirmReturnMode
		return m, nil
	}
	if !m.confirm.Submitted {
		return m, nil
	}
	choice := m.confirm.Choice
	kind := m.confirmKind
	m.confirm = nil
	m.confirmKind = ""
	m.mode = m.confirmReturnMode
	if choice == 0 {
		// User chose No — nothing to do.
		return m, nil
	}
	// Yes branch — dispatch by kind.
	switch {
	case kind == "requireSsh":
		m.requireSsh.Toggle()
		return m, nil
	case strings.HasPrefix(kind, "removeKey:"):
		idStr := strings.TrimPrefix(kind, "removeKey:")
		var id int64
		fmt.Sscanf(idStr, "%d", &id)
		if id > 0 {
			return m, m.deleteKey(id)
		}
	case strings.HasPrefix(kind, "removeLocation:"):
		idStr := strings.TrimPrefix(kind, "removeLocation:")
		var id int64
		fmt.Sscanf(idStr, "%d", &id)
		if id > 0 {
			return m, m.deleteLocation(id)
		}
	}
	return m, nil
}

//
// Finger key handling
//

func (m *Profile) handleFingerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q", "enter":
		return m, nav.Navigate(nav.DestLobby)
	}
	return m, nil
}

//
// View
//

func (m *Profile) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	if m.mode == modeFinger {
		return m.viewFinger()
	}
	if m.snap == nil {
		return lipgloss.Place(m.sess.Width, m.sess.Height-1, lipgloss.Center, lipgloss.Center,
			theme.Hint.Render("loading profile…"))
	}
	switch m.mode {
	case modeTabProfile, modeTabSettings:
		return m.renderTabs()
	case modePassword:
		base := m.renderTabs()
		dim := components.DimSGR(base, theme.ColorDim)
		modal := m.renderPasswordModal()
		return components.Overlay(dim, modal, m.sess.Width, m.availableHeight())
	case modeKeys:
		base := m.renderTabs()
		dim := components.DimSGR(base, theme.ColorDim)
		modal := m.renderKeysModal()
		return components.Overlay(dim, modal, m.sess.Width, m.availableHeight())
	case modeAddKey:
		base := m.renderTabs()
		dim := components.DimSGR(base, theme.ColorDim)
		modal := m.renderAddKeyModal()
		return components.Overlay(dim, modal, m.sess.Width, m.availableHeight())
	case modeLocations:
		base := m.renderTabs()
		dim := components.DimSGR(base, theme.ColorDim)
		modal := m.renderLocationsModal()
		return components.Overlay(dim, modal, m.sess.Width, m.availableHeight())
	case modeConfirm:
		base := m.renderTabs()
		dim := components.DimSGR(base, theme.ColorDim)
		modal := m.renderConfirmModal()
		return components.Overlay(dim, modal, m.sess.Width, m.availableHeight())
	}
	return ""
}

func (m *Profile) availableHeight() int {
	h := m.sess.Height - 1
	if h < 1 {
		h = 1
	}
	return h
}

// renderTabs renders the persistent tab strip + the active tab's body. The
// header is identical across the two tabs so the strip flickers minimally
// when switching.
func (m *Profile) renderTabs() string {
	tabRow := m.renderTabStrip()
	var body string
	switch m.mode {
	case modeTabProfile:
		body = m.viewProfileTab()
	case modeTabSettings:
		body = m.viewSettingsTab()
	}
	hint := m.renderTabHint()
	return lipgloss.JoinVertical(lipgloss.Left, tabRow, "", body, hint)
}

func (m *Profile) renderTabStrip() string {
	profile := profileTabIdleStyle.Render("Profile")
	settings := profileTabIdleStyle.Render("Settings")
	if m.mode == modeTabProfile {
		profile = profileTabActiveStyle.Render("Profile")
	}
	if m.mode == modeTabSettings {
		settings = profileTabActiveStyle.Render("Settings")
	}
	right := profileTabRightStyle.Render(fmt.Sprintf("@%s", m.sess.Identity.Handle))
	left := profile + "  " + settings
	w := m.sess.Width
	gap := w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return " " + left + strings.Repeat(" ", gap) + right + " "
}

func (m *Profile) renderTabHint() string {
	const hint = "Tab move · Ctrl+S save · Ctrl+T toggle tab · Esc lobby"
	hintStyled := profileHintStyle.Render(hint)
	if m.notice == "" {
		return "  " + hintStyled
	}
	noticeStyle := profileNoticeDim
	switch m.noticeKind {
	case "ok":
		noticeStyle = profileNoticeOk
	case "err":
		noticeStyle = profileNoticeErr
	}
	return "  " + noticeStyle.Render(m.notice) + "    " + hintStyled
}

// viewProfileTab renders the editable fields in a two-column layout: left
// column has the avatar placeholder + identity summary + action buttons,
// right column has the editable fields.
func (m *Profile) viewProfileTab() string {
	left := m.renderProfileLeft()
	right := m.renderProfileRight()
	return lipgloss.JoinHorizontal(lipgloss.Top, "  ", left, "    ", right)
}

func (m *Profile) renderProfileLeft() string {
	handle := "@" + m.snap.Handle
	if m.snap.IsSysop {
		handle += " " + profileSysopBadgeStyle.Render("SYSOP")
	}
	identity := profileIdentityStyle.Render(handle)
	joined := profileMutedStyle.Render("joined " + m.sess.DisplayPrefs.FormatDate(m.snap.CreatedAt))
	stats := profileMutedStyle.Render(fmt.Sprintf("%d chat · %d topics · %d posts",
		m.snap.ChatMessageCount, m.snap.TopicCount, m.snap.PostCount))

	pwLabel := "set password…"
	if m.snap.HasPassword {
		pwLabel = "change password…"
	}
	keysLabel := fmt.Sprintf("manage keys… (%d on file)", len(m.keys))
	viewLabel := "view as others see me…"
	locsLabel := "saved locations…"
	if m.sess.PrimaryLocation != nil {
		locsLabel = fmt.Sprintf("saved locations… (%s)", m.sess.PrimaryLocation.Label)
	}

	saveBtn := m.styleButton("save", m.focusIndex == 7)
	pwBtn := m.styleButton(pwLabel, m.focusIndex == 8)
	keysBtn := m.styleButton(keysLabel, m.focusIndex == 9)
	viewBtn := m.styleButton(viewLabel, m.focusIndex == 10)
	locsBtn := m.styleButton(locsLabel, m.focusIndex == 11)

	return lipgloss.JoinVertical(lipgloss.Left,
		identity,
		joined,
		stats,
		"",
		saveBtn,
		pwBtn,
		keysBtn,
		viewBtn,
		locsBtn,
	)
}

func (m *Profile) renderProfileRight() string {
	labelFor := func(idx int, text string) string {
		if m.focusIndex == idx {
			return profileActiveLabelStyle.Render(text)
		}
		return profileLabelStyle.Render(text)
	}

	tzView := m.tz.View(7)

	rows := []string{
		labelFor(0, "real name"),
		m.realName.View(),
		"",
		labelFor(1, "location"),
		m.location.View(),
		"",
		labelFor(2, "bio"),
		m.bio.View(),
		"",
		labelFor(3, "timezone"),
		tzView,
		"",
		labelFor(4, "temperature") + "  " + m.tempUnit.View(),
		labelFor(5, "clock      ") + "  " + m.clock.View(),
		labelFor(6, "date       ") + "  " + m.dateFmt.View(),
	}
	return strings.Join(rows, "\n")
}

// styleButton renders an inline button. Focused buttons get the highlight
// background; unfocused buttons read as dim chips.
func (m *Profile) styleButton(label string, focused bool) string {
	if focused {
		return profileButtonFocused.Render("▸ " + label)
	}
	return profileButtonIdle.Render("  " + label)
}

//
// Keys modal view
//

func (m *Profile) renderConfirmModal() string {
	if m.confirm == nil {
		return ""
	}
	w := m.sess.Width - 20
	if w > 60 {
		w = 60
	}
	if w < 40 {
		w = 40
	}
	return m.confirm.View(w)
}

