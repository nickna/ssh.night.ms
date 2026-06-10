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

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/auth/devicecode"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/providers/geocoding"
	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Profile is the TUI Profile destination — edit, password change, keys
// management, and finger all rolled into one tea.Model with an internal mode
// state machine (mirrors the Boards pattern).
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

	// locationSnapshot is what `location` was set to at hydrate time
	// (mirrors sess.PrimaryLocation.Label or the legacy users.location).
	// submitProfile compares against this to decide whether the location
	// side of the save needs to fire at all.
	locationSnapshot string

	// pickerResults / pickerQuery back modeProfileLocationPicker: the
	// geocode results we're asking the user to disambiguate, and the
	// originally-typed string we'll use as the saved-location label when
	// they pick.
	pickerResults []geocoding.Result
	pickerQuery   string

	// Settings tab inputs.
	suppressKeys components.Checkbox
	requireSsh   components.Checkbox

	// Locations modal state. savedLocations is the cached list; locCursor
	// is the row focus for delete. locAddOpen toggles the inline add form
	// (place + optional label); locFormFocus picks which input owns the
	// cursor (0 = place, 1 = label). locRenameOpen replaces the add form
	// with a single-input rename form when 'r' is pressed; locRenameID is
	// the target row. locSearchResults / locSearching back the geocoder
	// lookup fired on Enter; non-empty results are rendered as a 1-N
	// numbered picker above the form. locErr surfaces validation /
	// back-end errors above the form.
	savedLocations   []realtime.SavedLocation
	locCursor        int
	locAddOpen       bool
	locRenameOpen    bool
	locRenameID      int64
	locFormPlace     textinput.Model
	locFormLabel     textinput.Model
	locFormFocus     int
	locSearchResults []geocoding.Result
	locSearching     bool
	locErr           string

	// Password modal inputs.
	pwCurrent    textinput.Model
	pwNew        textinput.Model
	pwConfirm    textinput.Model
	pwFocusIndex int
	pwErr        string

	// Keys modal cursor.
	keysCursor int

	// OAuth modal state — list of Google/Microsoft accounts linked to this
	// SSH user, plus the device-code flow state while linking a new one.
	// oauthCreds is hydrated by oauthLoadedMsg; oauthFlow is non-nil only
	// while modeOAuthDevice is active.
	oauthCreds        []gen.ListOAuthCredentialsForUserRow
	oauthCursor       int
	oauthErr          string
	oauthBusy         bool
	oauthFlow         *devicecode.Flow
	oauthFlowProvider auth.OAuthProviderKind
	oauthFlowStatus   string

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
	modeOAuth                 // linked-accounts list
	modeOAuthAdd              // provider picker before kicking off a device flow
	modeOAuthDevice           // showing user code + polling
	modeProfileLocationPicker // geocoder disambiguation overlay during a Profile-tab save
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
	snap  *realtime.ProfileSnapshot
	keys  []gen.IdentityCredential
	oauth []gen.ListOAuthCredentialsForUserRow
	err   error
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

// profileGeocodeMsg lands when the Ctrl+S save's geocode lookup completes.
// Non-empty results enter modeProfileLocationPicker (or auto-accept the
// top hit when the AutoAccept heuristic fires); errors / empty results
// abort the save with an inline notice.
type profileGeocodeMsg struct {
	results []geocoding.Result
	err     error
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
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		snap, err := svc.GetByID(ctx, userID)
		if err != nil {
			return profileLoadedMsg{err: err}
		}
		keys, err := queries.ListSshCredentialsForUser(ctx, userID)
		if err != nil {
			return profileLoadedMsg{snap: snap, err: err}
		}
		// OAuth list is best-effort — a failure here shouldn't block the
		// profile page render. The list is hidden behind a button anyway.
		oauth, _ := queries.ListOAuthCredentialsForUser(ctx, userID)
		return profileLoadedMsg{snap: snap, keys: keys, oauth: oauth}
	}
}

// loadFinger fetches another user's snapshot for the read-only viewer.
// The viewer's display prefs come from the Session-cached DisplayPrefs
// (loaded at session-attach), so no extra round trip is needed here.
// The keys list isn't loaded — Finger mode never shows them.
func (m *Profile) loadFinger(handle string) tea.Cmd {
	svc := m.sess.Profile
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
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
	loc.Placeholder = "city name — used by Weather"
	// Source the field from the primary saved location when present so the
	// freeform textinput and the saved-locations list show the same truth.
	// Fall back to the legacy users.location string for users who haven't
	// re-saved since this unification.
	switch {
	case m.sess.PrimaryLocation != nil:
		loc.SetValue(m.sess.PrimaryLocation.Label)
	default:
		loc.SetValue(m.snap.Location)
	}
	m.location = loc
	// Snapshot the location value as it was hydrated so submitProfile can
	// tell whether the user actually edited it.
	m.locationSnapshot = loc.Value()

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
		m.oauthCreds = msg.oauth
		m.oauthCursor = clampIndex(m.oauthCursor, len(m.oauthCreds))
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
		refreshCtx, cancel := m.sess.CtxWithTimeout(2 * time.Second)
		if err := m.sess.RefreshDisplayPrefs(refreshCtx); err != nil {
			m.sess.Logger.Warn("refresh display prefs", "err", err)
		}
		// PrimaryLocation may have moved if the save included a
		// SetPrimaryFromGeocode / ClearPrimary side. Refresh it here so
		// WeatherCoords picks up the new state and the next hydrate
		// pre-populates the location field correctly.
		if err := m.sess.RefreshPrimaryLocation(refreshCtx); err != nil {
			m.sess.Logger.Warn("refresh primary location", "err", err)
		}
		cancel()
		// Re-load so derived fields (HasPassword, counts) refresh.
		return m, m.loadSelf()

	case profileGeocodeMsg:
		if msg.err != nil {
			m.working = false
			m.notice = "location lookup failed: " + msg.err.Error()
			m.noticeKind = "err"
			m.pickerQuery = ""
			m.pickerResults = nil
			return m, nil
		}
		if len(msg.results) == 0 {
			m.working = false
			m.notice = fmt.Sprintf("no places matched %q — try a broader name.", m.pickerQuery)
			m.noticeKind = "err"
			m.pickerQuery = ""
			return m, nil
		}
		// Auto-accept clear winners; otherwise enter the picker so the
		// user disambiguates Springfields and Parises.
		if r, ok := geocoding.AutoAccept(m.pickerQuery, msg.results); ok {
			m.pickerResults = nil
			return m, m.dispatchProfileSave(locationAction{
				kind:      locActionSetPrimary,
				label:     m.pickerQuery,
				canonical: r.Canonical(),
				lat:       r.Latitude,
				lon:       r.Longitude,
			})
		}
		m.working = false
		m.notice = ""
		m.noticeKind = ""
		m.pickerResults = msg.results
		m.mode = modeProfileLocationPicker
		return m, nil

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
		m.keysCursor = clampIndex(m.keysCursor, len(m.keys))
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
		m.locCursor = clampIndex(m.locCursor, len(m.savedLocations))
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
		// Auto-accept the top hit when it's an obvious winner; otherwise
		// show the picker so the user disambiguates.
		query := strings.TrimSpace(m.locFormPlace.Value())
		if r, ok := geocoding.AutoAccept(query, msg.results); ok {
			m.locSearchResults = nil
			m.locErr = ""
			return m, m.commitGeocodedLocation(*r)
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
		refreshCtx, cancel := m.sess.CtxWithTimeout(2 * time.Second)
		if err := m.sess.RefreshPrimaryLocation(refreshCtx); err != nil {
			m.sess.Logger.Warn("refresh primary location", "err", err)
		}
		cancel()
		return m, m.reloadLocations()

	case oauthListReloadedMsg:
		if msg.err != nil {
			m.oauthErr = "load failed: " + msg.err.Error()
			return m, nil
		}
		m.oauthCreds = msg.rows
		m.oauthCursor = clampIndex(m.oauthCursor, len(m.oauthCreds))
		return m, nil

	case oauthFlowStartedMsg:
		m.oauthBusy = false
		if msg.err != nil {
			m.oauthErr = oauthBeginErrorMessage(msg.err)
			m.mode = modeOAuthAdd
			return m, nil
		}
		m.oauthFlow = msg.flow
		m.oauthFlowProvider = msg.provider
		m.oauthFlowStatus = ""
		m.mode = modeOAuthDevice
		return m, m.scheduleOAuthPoll(msg.flow.Interval)

	case oauthPollTickMsg:
		// Stale tick after the user cancelled or the flow already
		// resolved — silently drop.
		if m.oauthFlow == nil || msg.flowID != m.oauthFlow.ID {
			return m, nil
		}
		return m, m.pollOAuthFlow()

	case oauthPollResultMsg:
		if msg.err != nil {
			m.oauthFlowStatus = "poll error: " + msg.err.Error()
			return m, m.scheduleOAuthPoll(3 * time.Second)
		}
		switch msg.result.Kind {
		case devicecode.ResultApproved:
			m.notice = fmt.Sprintf("%s account linked.", m.oauthFlowProvider)
			m.noticeKind = "ok"
			m.oauthFlow = nil
			m.oauthFlowStatus = ""
			m.mode = modeOAuth
			return m, m.reloadOAuth()
		case devicecode.ResultDuplicate, devicecode.ResultDenied, devicecode.ResultExpired:
			m.oauthFlowStatus = oauthStatusForResult(msg.result.Kind)
			return m, nil
		case devicecode.ResultPending, devicecode.ResultSlowDown:
			m.oauthFlowStatus = oauthStatusForResult(msg.result.Kind)
			return m, m.scheduleOAuthPoll(msg.result.NextPollAfter)
		}
		return m, nil

	case oauthUnlinkedMsg:
		if msg.err != nil {
			m.notice = "unlink failed: " + msg.err.Error()
			m.noticeKind = "err"
			return m, nil
		}
		m.notice = "connected account unlinked."
		m.noticeKind = "ok"
		return m, m.reloadOAuth()

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
	case modeOAuth:
		return m.handleOAuthKey(k)
	case modeOAuthAdd:
		return m.handleOAuthAddKey(k)
	case modeOAuthDevice:
		return m.handleOAuthDeviceKey(k)
	case modeProfileLocationPicker:
		return m.handleProfileLocationPickerKey(k)
	}
	return m, nil
}

// handleProfileLocationPickerKey owns the geocoder-disambiguation overlay
// shown during a Profile-tab save. Digits 1-N pick the corresponding row;
// Enter commits the top result (results[0]); Esc cancels the entire save,
// leaving the user's edited fields in place so they can retry. Mirrors
// the digit-pick UX from the Saved Locations modal at
// profile_locations.go:243.
func (m *Profile) handleProfileLocationPickerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.pickerResults) == 0 {
		// Stale state — bail back to the profile tab.
		m.mode = modeTabProfile
		return m, nil
	}
	switch s := k.String(); s {
	case "esc":
		m.mode = modeTabProfile
		m.pickerResults = nil
		m.pickerQuery = ""
		m.notice = "save cancelled."
		m.noticeKind = ""
		return m, nil
	case "enter":
		r := m.pickerResults[0]
		return m.commitPickedLocation(r)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(s[0] - '1')
		if idx < len(m.pickerResults) {
			return m.commitPickedLocation(m.pickerResults[idx])
		}
	}
	return m, nil
}

// commitPickedLocation finalizes the geocode pick — saves the row via
// SetPrimaryFromGeocode + UpdateProfile, then drops back to modeTabProfile
// while the dispatched cmd is in flight. profileSavedMsg lands the
// success/error toast.
func (m *Profile) commitPickedLocation(r geocoding.Result) (tea.Model, tea.Cmd) {
	label := strings.TrimSpace(m.pickerQuery)
	if label == "" {
		label = r.Name
	}
	m.pickerResults = nil
	m.mode = modeTabProfile
	return m, m.dispatchProfileSave(locationAction{
		kind:      locActionSetPrimary,
		label:     label,
		canonical: r.Canonical(),
		lat:       r.Latitude,
		lon:       r.Longitude,
	})
}

// profileTabStops is the number of focusable widgets on the profile tab.
// Order: realName, location, bio, tz, tempUnit, clock, dateFmt,
// SaveBtn, PasswordBtn, KeysBtn, ViewSelfBtn, LocationsBtn, ConnectionsBtn.
const profileTabStops = 13

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
	profileNoticeBase = lipgloss.NewStyle().Bold(true)
	profileNoticeOk   = profileNoticeBase.Foreground(lipgloss.Color(theme.ColorGreen))
	profileNoticeErr  = profileNoticeBase.Foreground(lipgloss.Color(theme.ColorRed))
	profileNoticeDim  = profileNoticeBase.Foreground(lipgloss.Color(theme.ColorAccentDim))

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
		case 12:
			return m, m.openOAuth()
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

// submitProfile is the entry into the multi-stage save state machine. The
// location field is a thin editor for the user's primary saved location;
// changing it triggers a geocode + auto-accept-or-pick before the other
// fields land through ProfileService.UpdateProfile. Three branches:
//
//   - location unchanged → fire UpdateProfile directly (today's behavior).
//   - location cleared with an existing primary → confirm overlay; on Yes
//     run ClearPrimary then UpdateProfile.
//   - location changed to a non-empty value → geocode, disambiguate via
//     modeProfileLocationPicker when needed, run SetPrimaryFromGeocode
//     then UpdateProfile.
//
// All three converge on profileSavedMsg so the existing post-save
// refresh-and-reload logic stays untouched.
func (m *Profile) submitProfile() tea.Cmd {
	if m.snap == nil {
		return nil
	}
	if m.working {
		return nil
	}

	newLoc := strings.TrimSpace(m.location.Value())
	prevLoc := strings.TrimSpace(m.locationSnapshot)

	switch {
	case newLoc == prevLoc:
		// No location-side work; fire the flat update.
		return m.dispatchProfileSave(locationAction{kind: locActionNoop})
	case newLoc == "" && m.sess.PrimaryLocation != nil:
		// User blanked the field — confirm before deleting the primary row.
		m.confirm = components.NewConfirm(
			"remove primary location",
			fmt.Sprintf("remove %q? Weather will stop working until you set a new location.", m.sess.PrimaryLocation.Label),
		)
		m.confirmKind = "clearPrimaryLocation"
		m.confirmReturnMode = modeTabProfile
		m.mode = modeConfirm
		return nil
	case newLoc == "":
		// Empty with no existing primary — nothing to clear; flat update.
		return m.dispatchProfileSave(locationAction{kind: locActionNoop})
	default:
		// Geocode the typed string; the result handler decides whether to
		// auto-accept or show the picker.
		m.working = true
		m.notice = "looking up location…"
		m.noticeKind = ""
		m.pickerQuery = newLoc
		svc := m.sess.Geocoder
		query := newLoc
		return func() tea.Msg {
			ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
			defer cancel()
			if svc == nil {
				return profileGeocodeMsg{err: errors.New("geocoder unavailable")}
			}
			results, err := svc.Search(ctx, query, 5)
			return profileGeocodeMsg{results: results, err: err}
		}
	}
}

// locActionKind discriminates the union below. locActionNoop skips the
// saved-locations side of the save entirely.
type locActionKind int

const (
	locActionNoop locActionKind = iota
	locActionSetPrimary
	locActionClearPrimary
)

// locationAction is the saved-locations work the chained save cmd will
// perform before firing UpdateProfile. Stays internal to the screen.
type locationAction struct {
	kind      locActionKind
	label     string
	canonical string
	lat       float64
	lon       float64
}

// dispatchProfileSave returns the single tea.Cmd that does both the
// location-side work (per `act`) AND the other-fields UpdateProfile call,
// folding any error into a single profileSavedMsg. Sequencing both inside
// one goroutine keeps the message graph small and means "saving…" cleanly
// flips to "saved." once the whole transaction has landed.
func (m *Profile) dispatchProfileSave(act locationAction) tea.Cmd {
	m.working = true
	m.notice = "saving…"
	m.noticeKind = ""
	update := m.buildProfileUpdate()
	profSvc := m.sess.Profile
	locSvc := m.sess.Locations
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(8 * time.Second)
		defer cancel()
		switch act.kind {
		case locActionSetPrimary:
			if locSvc == nil {
				return profileSavedMsg{err: errors.New("location service unavailable")}
			}
			if _, err := locSvc.SetPrimaryFromGeocode(ctx, userID, act.label, act.canonical, act.lat, act.lon); err != nil {
				return profileSavedMsg{err: err}
			}
		case locActionClearPrimary:
			if locSvc == nil {
				return profileSavedMsg{err: errors.New("location service unavailable")}
			}
			if err := locSvc.ClearPrimary(ctx, userID); err != nil {
				return profileSavedMsg{err: err}
			}
		}
		if err := profSvc.UpdateProfile(ctx, userID, update); err != nil {
			return profileSavedMsg{err: err}
		}
		return profileSavedMsg{}
	}
}

// buildProfileUpdate snapshots the current widget state into the
// ProfileUpdate struct that UpdateProfile takes. Location is intentionally
// omitted — the saved-locations layer owns that text now.
func (m *Profile) buildProfileUpdate() realtime.ProfileUpdate {
	update := realtime.ProfileUpdate{
		RealName:                   strings.TrimSpace(m.realName.Value()),
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
	return update
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
	case kind == "clearPrimaryLocation":
		return m, m.dispatchProfileSave(locationAction{kind: locActionClearPrimary})
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
	case strings.HasPrefix(kind, "removeOAuth:"):
		idStr := strings.TrimPrefix(kind, "removeOAuth:")
		var id int64
		fmt.Sscanf(idStr, "%d", &id)
		if id > 0 {
			return m, m.unlinkOAuth(id)
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
	case modeOAuth:
		base := m.renderTabs()
		dim := components.DimSGR(base, theme.ColorDim)
		modal := m.renderOAuthModal()
		return components.Overlay(dim, modal, m.sess.Width, m.availableHeight())
	case modeOAuthAdd:
		base := m.renderTabs()
		dim := components.DimSGR(base, theme.ColorDim)
		modal := m.renderOAuthAddModal()
		return components.Overlay(dim, modal, m.sess.Width, m.availableHeight())
	case modeOAuthDevice:
		base := m.renderTabs()
		dim := components.DimSGR(base, theme.ColorDim)
		modal := m.renderOAuthDeviceModal()
		return components.Overlay(dim, modal, m.sess.Width, m.availableHeight())
	case modeProfileLocationPicker:
		base := m.renderTabs()
		dim := components.DimSGR(base, theme.ColorDim)
		modal := m.renderProfileLocationPickerModal()
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
	oauthLabel := "connected accounts…"
	if n := len(m.oauthCreds); n > 0 {
		oauthLabel = fmt.Sprintf("connected accounts… (%d linked)", n)
	}

	saveBtn := m.styleButton("save", m.focusIndex == 7)
	pwBtn := m.styleButton(pwLabel, m.focusIndex == 8)
	keysBtn := m.styleButton(keysLabel, m.focusIndex == 9)
	viewBtn := m.styleButton(viewLabel, m.focusIndex == 10)
	locsBtn := m.styleButton(locsLabel, m.focusIndex == 11)
	oauthBtn := m.styleButton(oauthLabel, m.focusIndex == 12)

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
		oauthBtn,
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

	locationHint := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).
		Render("type a city — Weather + Map use this as your primary location")

	rows := []string{
		labelFor(0, "real name"),
		m.realName.View(),
		"",
		labelFor(1, "location"),
		m.location.View(),
		locationHint,
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

// renderProfileLocationPickerModal renders the disambiguation overlay
// shown during a Profile-tab save when AutoAccept rejected the top hit.
// Mirrors the digit-pick affordance the Saved Locations modal uses; the
// label and hint line clarify what selecting a row will do — it's the
// last step of the user's Ctrl+S save, not a parallel "search" affordance.
func (m *Profile) renderProfileLocationPickerModal() string {
	innerW := m.sess.Width - 12
	if innerW > 80 {
		innerW = 80
	}
	if innerW < 50 {
		innerW = 50
	}
	header := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).
		Render(fmt.Sprintf("which %q did you mean?", m.pickerQuery))
	blurb := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).Width(innerW).
		Render("Press a number to pick. The chosen place becomes your primary saved location and Weather uses it from now on.")

	rows := []string{header, blurb, ""}
	numStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	for i, r := range m.pickerResults {
		rows = append(rows, fmt.Sprintf("  %s  %s  (%.4f, %.4f)",
			numStyle.Render(fmt.Sprintf("%d", i+1)),
			r.Canonical(), r.Latitude, r.Longitude))
	}
	rows = append(rows, "", lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).
		Render("1-N pick · Enter take #1 · Esc cancel save"))

	return theme.ModalFrame.Width(innerW + 6).Render(strings.Join(rows, "\n"))
}

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
