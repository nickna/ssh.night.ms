package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/imaging/graphics"
	"github.com/nickna/ssh.night.ms/internal/tui"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
)

// wsBridgeReadWriter adapts a websocket.Conn to io.ReadWriter so we can hand
// it straight to bubbletea via tea.WithInput / tea.WithOutput. Binary frames
// carry the terminal byte stream both directions; text frames are reserved
// for JSON control messages (resize, etc.) and parsed by a separate goroutine.
type wsBridgeReadWriter struct {
	conn *websocket.Conn
	ctx  context.Context

	// readBuf carries the tail of a binary frame we partially returned in a
	// previous Read call. bubbletea's input loop calls Read with a small
	// buffer, but a single keystroke (especially escape sequences) may span
	// several Read calls.
	readBuf []byte
	readMu  sync.Mutex

	// writeMu serializes Write calls so concurrent bubbletea draws don't
	// interleave bytes in flight to the same WS frame.
	writeMu sync.Mutex
}

// Read returns terminal-byte-stream bytes from the next binary WS frame.
// Text frames (control messages) are skipped — they're handled by the
// resize loop in HandleBBS instead.
func (w *wsBridgeReadWriter) Read(p []byte) (int, error) {
	w.readMu.Lock()
	defer w.readMu.Unlock()
	if len(w.readBuf) > 0 {
		n := copy(p, w.readBuf)
		w.readBuf = w.readBuf[n:]
		return n, nil
	}
	for {
		mt, data, err := w.conn.Read(w.ctx)
		if err != nil {
			return 0, err
		}
		if mt != websocket.MessageBinary {
			// Control frames are read by the parallel JSON loop; ignore them
			// here so they don't get fed to bubbletea as random bytes.
			continue
		}
		n := copy(p, data)
		if n < len(data) {
			w.readBuf = data[n:]
		}
		return n, nil
	}
}

// Write sends ANSI/escape bytes from bubbletea as one binary WS frame.
// bubbletea typically calls Write with a complete redraw payload, so the
// frame size stays manageable.
func (w *wsBridgeReadWriter) Write(p []byte) (int, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if err := w.conn.Write(w.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// wsControlMsg is the JSON shape sent on text frames. Keep the field set
// small — we expand as needed in later phases (e.g., for clipboard hooks).
type wsControlMsg struct {
	Type string `json:"type"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// HandleBBS upgrades the request to a WebSocket and runs a fresh tea.Program
// against it for the authenticated user. Bypasses wish entirely — the cookie
// session is enough; we already trust the identity from /login.
//
// On disconnect (client close or program quit) the goroutine ends cleanly.
func (h *handlers) handleBBSWebSocket(w http.ResponseWriter, r *http.Request) {
	identity := identityFrom(r)
	if identity == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{"nightms.bbs.v1"},
		// Explicit Origin allowlist so a hosted page at evil.example can't
		// open a bridge to our session even with a valid cookie smuggled
		// across origins. coder/websocket defaults to same-origin only,
		// which today matches PublicHost; encoding the list explicitly
		// makes the policy local-visible and prevents a future PublicHost
		// change from accidentally widening the scope.
		OriginPatterns: h.wsOriginPatterns(),
		// Read frames cap at 1 MiB — way more than any keystroke needs but
		// lets terminal pastes through without surprise rejections.
		// CompressionMode default is good.
	})
	if err != nil {
		h.deps.Logger.Warn("wsbridge: accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	// First text frame should be the initial resize so we know what size to
	// build the tea.Program at. Bail with a small grace window — clients
	// that don't send one within 3s get a default 80×24.
	cols, rows := h.waitForInitialResize(conn, r.Context(), 3*time.Second)

	known := auth.Known{
		UserID:  identity.UserID,
		Handle:  identity.Handle,
		IsSysop: identity.IsSysop,
	}

	bridgeCtx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Global session cap mirrors the SSH path — settings.MaxTotalSessions
	// covers both surfaces so a sysop tightening it during an incident closes
	// new logins everywhere at once.
	var maxTotal int
	if h.deps.Session.Security.Settings != nil {
		maxTotal = h.deps.Session.Security.Settings.Get().MaxTotalSessions
	}
	if !session.AcquireForContext(bridgeCtx, maxTotal) {
		h.deps.Logger.Warn("wsbridge session rejected: total session cap reached",
			"handle", identity.Handle, "cap", maxTotal, "active", session.ActiveCount())
		_ = conn.Close(websocket.StatusTryAgainLater, "server at capacity")
		return
	}

	st := session.State{
		Identity: known,
		Width:    cols,
		Height:   rows,
	}
	// Pre-warm primary location + display prefs the same way transport does,
	// so weather screens and the status-bar clock render consistently across
	// SSH and the WebSocket bridge.
	if h.deps.Locations != nil {
		loadCtx, loadCancel := context.WithTimeout(r.Context(), 2*time.Second)
		if loc, err := h.deps.Locations.GetPrimary(loadCtx, known.UserID); err != nil {
			h.deps.Logger.Warn("wsbridge: primary location load", "handle", known.Handle, "err", err)
		} else {
			st.PrimaryLocation = loc
		}
		loadCancel()
	}
	if h.deps.Queries != nil {
		loadCtx, loadCancel := context.WithTimeout(r.Context(), 2*time.Second)
		if user, err := h.deps.Queries.GetUserByID(loadCtx, known.UserID); err != nil {
			h.deps.Logger.Warn("wsbridge: display prefs load", "handle", known.Handle, "err", err)
		} else {
			st.DisplayPrefs = session.DisplayPrefsFromUser(user)
			st.ProfileLocation = session.ProfileLocationFromUser(user)
		}
		loadCancel()
	}
	// One-shot backfill of user_saved_locations from the legacy
	// users.location_* columns. Mirrors the same block in transport so
	// both surfaces converge on a single saved-location row after first
	// login. SeedFromProfile is internally idempotent (re-checks the list)
	// so a racing SSH + web login won't double-insert.
	if st.PrimaryLocation == nil && st.ProfileLocation != nil && h.deps.Locations != nil {
		seedCtx, seedCancel := context.WithTimeout(r.Context(), 2*time.Second)
		if seeded, err := h.deps.Locations.SeedFromProfile(seedCtx, known.UserID,
			st.ProfileLocation.Label, st.ProfileLocation.Canonical,
			st.ProfileLocation.Lat, st.ProfileLocation.Lon); err != nil {
			h.deps.Logger.Warn("wsbridge: profile-location backfill", "handle", known.Handle, "err", err)
		} else if seeded != nil {
			st.PrimaryLocation = seeded
		}
		seedCancel()
	}
	// Web clients can't negotiate kitty/iTerm/sixel — xterm.js renders the
	// half-block fallback every time.
	sess := session.New(h.deps.Session, st, bridgeCtx, graphics.Halfblock)

	// Start the per-session presence heartbeat exactly like the SSH path does.
	if h.deps.Presence != nil {
		go h.deps.Presence.RunHeartbeat(r.Context(), known.Handle, known.UserID)
	}

	rw := &wsBridgeReadWriter{conn: conn, ctx: bridgeCtx}
	program := tea.NewProgram(
		tui.NewRoot(sess),
		tea.WithInput(rw),
		tea.WithOutput(rw),
		tea.WithoutSignalHandler(),
		tea.WithAltScreen(),
		// xterm.js sends mouse events the same way real terminals do; opt in
		// so screens can listen for tea.MouseMsg (e.g., click-to-switch on
		// the chat sidebar).
		tea.WithMouseCellMotion(),
	)
	// Stash the program on the session so screens that need ReleaseTerminal /
	// RestoreTerminal (browser rich-mode) can reach it. IsSSH stays false so
	// the rich-mode gate short-circuits on this path — there's no PTY to give
	// Carbonyl, and xterm.js wouldn't answer its terminal-capability probes.
	sess.SetTeaProgram(program)

	// Forward subsequent resize frames to the program via tea.Send.
	go h.forwardResizes(bridgeCtx, conn, program)

	if _, err := program.Run(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		h.deps.Logger.Info("wsbridge: program exited", "handle", known.Handle, "err", err)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// wsOriginPatterns returns the Origin allowlist passed to the WebSocket
// upgrade. Includes the configured PublicHost plus localhost on the server's
// HTTP port so dev/smoke tests over plain HTTP work without extra config.
// Each pattern is a hostname or host:port; coder/websocket matches the
// browser's Origin header against this list (case-insensitive).
func (h *handlers) wsOriginPatterns() []string {
	port := portFromAddr(h.cfg.Addr)
	patterns := []string{h.cfg.PublicHost}
	if port != "" {
		patterns = append(patterns,
			"localhost:"+port,
			"127.0.0.1:"+port,
		)
	}
	patterns = append(patterns, "localhost", "127.0.0.1")
	return patterns
}

// waitForInitialResize reads text frames until one carries a type:"resize" or
// the deadline expires. Returns sensible defaults if no resize arrives in
// time so the user still sees something usable.
func (h *handlers) waitForInitialResize(conn *websocket.Conn, parent context.Context, timeout time.Duration) (int, int) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			return 80, 24
		}
		if mt != websocket.MessageText {
			continue
		}
		var msg wsControlMsg
		if json.Unmarshal(data, &msg) != nil || msg.Type != "resize" {
			continue
		}
		cols, rows := clampDims(msg.Cols, msg.Rows)
		return cols, rows
	}
}

// forwardResizes reads text frames for the lifetime of the bridge and pushes
// every resize into the bubbletea program as a WindowSizeMsg.
func (h *handlers) forwardResizes(ctx context.Context, conn *websocket.Conn, program *tea.Program) {
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if mt != websocket.MessageText {
			continue
		}
		var msg wsControlMsg
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "resize":
			cols, rows := clampDims(msg.Cols, msg.Rows)
			program.Send(tea.WindowSizeMsg{Width: cols, Height: rows})
		}
	}
}

// clampDims keeps obviously-bogus client values from breaking layouts (a
// resize to 0×0 would zero the chat sidebar width math, etc.).
func clampDims(cols, rows int) (int, int) {
	if cols < 20 {
		cols = 20
	}
	if cols > 500 {
		cols = 500
	}
	if rows < 5 {
		rows = 5
	}
	if rows > 200 {
		rows = 200
	}
	return cols, rows
}

// terminalPage renders the xterm.js host page. Requires an authenticated
// session — anonymous users get bounced to /login.
func (h *handlers) terminalPage(w http.ResponseWriter, r *http.Request) {
	if identityFrom(r) == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	h.render(w, "terminal", h.basePage(r, "terminal"))
}

// Compile-time sanity: wsBridgeReadWriter must satisfy io.ReadWriter for tea.WithInput/Output.
var _ io.ReadWriter = (*wsBridgeReadWriter)(nil)

// Unused but kept so we don't have to dance with fmt being trimmed when
// debug logging is removed in a refactor.
var _ = fmt.Sprintf
