using Night.Ms.SshServer.Tui.StatusBar;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui;

// Common base for every BBS screen: applies the BbsTheme color scheme and pins a persistent
// BbsStatusBar to the bottom row. Subclasses don't have to remember to call ApplyWindow or
// add a footer — they get both for free. The bottom row of the viewport is owned by the
// footer, so subclasses must leave Dim.Fill(1) (or more) of vertical headroom.
public abstract class BbsWindow : Window
{
    protected BbsStatusBar StatusBar { get; }

    protected BbsWindow(IApplication app, IServiceProvider services)
    {
        BbsTheme.ApplyWindow(this);

        StatusBar = new BbsStatusBar(app, services)
        {
            X = 0,
            Y = Pos.AnchorEnd(1),
            Width = Dim.Fill(),
            Height = 1,
        };
        Add(StatusBar);
    }
}
