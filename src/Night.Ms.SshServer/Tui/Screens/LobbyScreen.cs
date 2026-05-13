using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public enum LobbyNavigation { Chat, Boards, Profile, News, Browser, Gallery, Map, Sysop, Logout }

public sealed class LobbyScreen : BbsWindow
{
    private readonly IApplication _app;

    // The button row + its key bindings used to live in two parallel hand-rolled lists. They're
    // table-driven now so a tenth destination is one row and the layout-chain skip for invisible
    // buttons (e.g. _Sysop for non-sysops) happens in one place.
    private readonly record struct LobbyEntry(
        string Label,
        Key Hotkey,
        LobbyNavigation Target,
        bool Visible = true,
        bool IsDefault = false);

    public LobbyScreen(IApplication app, IServiceProvider services, User user, bool justRegistered, ArtProvider art)
        : base(app, services, user)
    {
        _app = app;
        Title = $"ssh.night.ms — lobby — {user.Handle}";

        View artView;
        if (art.IsColor)
        {
            artView = new AnsiArtView { X = 0, Y = 0, Grid = art.Grid };
        }
        else
        {
            var label = new Label { X = 0, Y = 0, Text = art.Art };
            label.SetScheme(BbsTheme.Hint);
            artView = label;
        }

        // Push the rest of the lobby below the art (with a one-row gap).
        var contentTop = art.LineCount + 1;

        var welcome = new Label
        {
            X = 2,
            Y = contentTop,
            Text = justRegistered
                ? $"Welcome aboard, {user.Handle}. Your key is bound to this account."
                : $"Welcome back, {user.Handle}.",
        };
        welcome.SetScheme(justRegistered ? BbsTheme.Success_ : BbsTheme.Header_);

        var hint = new Label
        {
            X = 2,
            Y = contentTop + 2,
            Text = "Choose where to go:",
        };
        hint.SetScheme(BbsTheme.Hint);

        Add(artView, welcome, hint);

        var entries = new[]
        {
            new LobbyEntry("_Chat (#lobby)", Key.C, LobbyNavigation.Chat, IsDefault: true),
            new LobbyEntry("_Boards",        Key.B, LobbyNavigation.Boards),
            new LobbyEntry("_Profile",       Key.P, LobbyNavigation.Profile),
            new LobbyEntry("_News",          Key.N, LobbyNavigation.News),
            new LobbyEntry("Bro_wser",       Key.W, LobbyNavigation.Browser),
            new LobbyEntry("_Gallery",       Key.G, LobbyNavigation.Gallery),
            new LobbyEntry("_Map",           Key.M, LobbyNavigation.Map),
            new LobbyEntry("_Sysop",         Key.S, LobbyNavigation.Sysop, Visible: user.IsSysop),
            new LobbyEntry("_Logout",        Key.L, LobbyNavigation.Logout),
        };

        Button? prevVisible = null;
        foreach (var entry in entries)
        {
            var btn = new Button
            {
                X = prevVisible is null ? 2 : Pos.Right(prevVisible) + 2,
                Y = contentTop + 4,
                Text = entry.Label,
                IsDefault = entry.IsDefault,
                Visible = entry.Visible,
                Enabled = entry.Visible,
            };
            var target = entry.Target;
            btn.Accepting += (_, e) => { e.Handled = true; Choose(target); };
            Add(btn);
            if (entry.Visible) prevVisible = btn;
        }

        var sysopBadge = new Label
        {
            X = 2,
            Y = contentTop + 7,
            Text = user.IsSysop ? "[ sysop access granted — press S for the console ]" : string.Empty,
        };
        sysopBadge.SetScheme(BbsTheme.Success_);
        Add(sysopBadge);

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)        { key.Handled = true; Choose(LobbyNavigation.Logout); return; }
            // Enter from anywhere on the lobby jumps into chat — saves a Tab dance.
            if (key == Key.Enter)      { key.Handled = true; Choose(LobbyNavigation.Chat); return; }

            foreach (var entry in entries)
            {
                if (!entry.Visible) continue;
                if (key == entry.Hotkey || key == entry.Hotkey.WithShift)
                {
                    key.Handled = true;
                    Choose(entry.Target);
                    return;
                }
            }
        };
    }

    private void Choose(LobbyNavigation target)
    {
        Result = target;
        _app.RequestStop();
    }
}
