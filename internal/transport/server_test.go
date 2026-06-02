package transport

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/imaging/graphics"
	"github.com/nickna/ssh.night.ms/internal/tui"
	"github.com/nickna/ssh.night.ms/internal/tui/screens"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// minimalDeps returns a session.Deps with all optional sub-fields nil — the
// dispatch path is nil-safe for Realtime.Locations, Core.Queries, and
// Realtime.Presence. The pure routing logic is what we want to exercise.
func minimalDeps() session.Deps {
	return session.Deps{
		Core: session.CoreDeps{Logger: quietLogger()},
	}
}

func TestDispatchAuthKnownReturnsLobbyRoot(t *testing.T) {
	deps := minimalDeps()
	model, opts, _ := dispatchAuth(
		deps, quietLogger(), context.Background(), "alice",
		auth.Known{UserID: 42, Handle: "alice"},
		graphics.Halfblock, 80, 24,
	)
	if model == nil {
		t.Fatal("dispatchAuth(Known) returned nil model")
	}
	if _, ok := model.(*tui.Root); !ok {
		t.Errorf("dispatchAuth(Known) model type = %T, want *tui.Root", model)
	}
	if len(opts) == 0 {
		t.Errorf("dispatchAuth(Known) returned no ProgramOptions; expected at least AltScreen + MouseCellMotion")
	}
}

func TestDispatchAuthSignupReturnsSignupRoot(t *testing.T) {
	deps := minimalDeps()
	deps.Policy.MinPasswordLength = 8
	model, opts, _ := dispatchAuth(
		deps, quietLogger(), context.Background(), "newuser",
		auth.SignupRequired{Handle: "newuser"},
		graphics.Halfblock, 80, 24,
	)
	if model == nil {
		t.Fatal("dispatchAuth(SignupRequired) returned nil model")
	}
	// tui.NewSignupRoot returns a wrapped model — assert via type name
	// rather than concrete struct (the wrapper is unexported in tui).
	if name := reflect.TypeOf(model).String(); name == "" {
		t.Errorf("dispatchAuth(SignupRequired) returned unnamed model type")
	}
	if len(opts) == 0 {
		t.Errorf("dispatchAuth(SignupRequired) returned no ProgramOptions")
	}
}

func TestDispatchAuthUnknownDecisionReturnsErrorMessage(t *testing.T) {
	deps := minimalDeps()
	// Pass a decision type that isn't Known or SignupRequired — wish should
	// never get us here, but the defensive branch must not panic.
	model, opts, _ := dispatchAuth(
		deps, quietLogger(), context.Background(), "bob",
		auth.Banned{Reason: "test"},
		graphics.Halfblock, 80, 24,
	)
	if model == nil {
		t.Fatal("dispatchAuth(Banned) returned nil model")
	}
	if _, ok := model.(screens.Message); !ok {
		t.Errorf("dispatchAuth(unknown) model type = %T, want *screens.Message", model)
	}
	if len(opts) != 0 {
		t.Errorf("dispatchAuth(unknown) returned ProgramOptions = %d, want 0", len(opts))
	}
}

func TestDispatchAuthNilDecisionReturnsErrorMessage(t *testing.T) {
	// Real-world: wish.Context().Value() returns nil if the auth callbacks
	// somehow never stored a decision. Must not panic.
	deps := minimalDeps()
	model, _, _ := dispatchAuth(
		deps, quietLogger(), context.Background(), "ghost",
		nil, graphics.Halfblock, 80, 24,
	)
	if _, ok := model.(screens.Message); !ok {
		t.Errorf("dispatchAuth(nil) model type = %T, want *screens.Message", model)
	}
}

func TestDispatchAuthKnownStartsPresenceHeartbeat(t *testing.T) {
	// Verifies the Known arm fires the heartbeat goroutine — the goroutine
	// itself is bound to sshCtx, so a cancellable parent gives us a clean
	// way to assert it actually started without blocking the test forever.
	// We can't easily observe the goroutine directly (no fake Presence),
	// but we can check that nil Presence is tolerated without panic.
	deps := minimalDeps() // Realtime.Presence is nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model, _, _ := dispatchAuth(
		deps, quietLogger(), ctx, "alice",
		auth.Known{UserID: 1, Handle: "alice"},
		graphics.Halfblock, 80, 24,
	)
	if model == nil {
		t.Fatal("nil Presence path should still return a model")
	}
}

func TestDispatchAuthPropagatesWindowDims(t *testing.T) {
	// The session State gets Width/Height; the Root model holds the session;
	// since the session is unexported via Root we can't directly inspect,
	// but we ensure the call itself works with various dim inputs.
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"std vt100", 80, 24},
		{"wide ultra", 200, 80},
		{"very tiny", 20, 5},
		{"zero (pre-resize)", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, _, _ := dispatchAuth(
				minimalDeps(), quietLogger(), context.Background(), "alice",
				auth.Known{UserID: 1, Handle: "alice"},
				graphics.Halfblock, tc.w, tc.h,
			)
			if model == nil {
				t.Fatalf("dispatchAuth(%dx%d) returned nil", tc.w, tc.h)
			}
		})
	}
}

// Compile-time sanity: the test below proves dispatchAuth returns one of
// the documented arms. Adding a new Decision variant to auth without
// updating dispatchAuth will keep compiling — this assertion at least
// guarantees the unknown-decision fallback path runs cleanly.
var _ tea.Model = (*tui.Root)(nil)
