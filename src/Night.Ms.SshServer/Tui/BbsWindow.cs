using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui.StatusBar;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui;

// Common base for every BBS screen: applies the BbsTheme color scheme and pins a persistent
// BbsStatusBar to the bottom row. Subclasses don't have to remember to call ApplyWindow or
// add a footer — they get both for free. The bottom row of the viewport is owned by the
// footer, so subclasses must leave Dim.Fill(1) (or more) of vertical headroom.
//
// `user` is nullable so the pre-login RegisterScreen (which has no User row yet) can render —
// the status bar then formats time and weather using global defaults (UTC, °C, 24h, ISO).
public abstract class BbsWindow : Window
{
    // BbsWindow keeps its own non-null IApplication reference rather than relying on the
    // inherited View.App, which is nullable and only populated once the view is wired into
    // a SuperView chain — by then ctor-time wiring helpers like InstallEscapeHandler have
    // already needed it.
    private readonly IApplication _app;

    protected BbsStatusBar StatusBar { get; }

    protected BbsWindow(IApplication app, IServiceProvider services, User? user)
    {
        _app = app;
        BbsTheme.ApplyWindow(this);

        StatusBar = new BbsStatusBar(app, services, user)
        {
            X = 0,
            Y = Pos.AnchorEnd(1),
            Width = Dim.Fill(),
            Height = 1,
        };
        Add(StatusBar);
    }

    // Wires Esc → optional cleanup → App.RequestStop. Use for screens whose only Esc
    // semantics are "leave this screen." Screens that bind Esc alongside Q/Shift+Q or
    // dispatch Esc through a state machine (LobbyScreen, viewer screens) keep their own
    // handlers.
    protected void InstallEscapeHandler(Action? onEscape = null)
    {
        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                onEscape?.Invoke();
                _app.RequestStop();
                key.Handled = true;
            }
        };
    }
}
