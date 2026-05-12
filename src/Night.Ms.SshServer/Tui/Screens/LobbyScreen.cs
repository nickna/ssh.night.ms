using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public enum LobbyNavigation { Chat, Boards, Profile, News, Gallery, Sysop, Logout }

public sealed class LobbyScreen : BbsWindow
{
    private readonly IApplication _app;

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

        var chat = new Button
        {
            X = 2,
            Y = contentTop + 4,
            Text = "_Chat (#lobby)",
            IsDefault = true,
        };
        chat.Accepting += (_, e) =>
        {
            e.Handled = true;
            Result = LobbyNavigation.Chat;
            _app.RequestStop();
        };

        var boards = new Button
        {
            X = Pos.Right(chat) + 2,
            Y = contentTop + 4,
            Text = "_Boards",
        };
        boards.Accepting += (_, e) =>
        {
            e.Handled = true;
            Result = LobbyNavigation.Boards;
            _app.RequestStop();
        };

        var profile = new Button
        {
            X = Pos.Right(boards) + 2,
            Y = contentTop + 4,
            Text = "_Profile",
        };
        profile.Accepting += (_, e) =>
        {
            e.Handled = true;
            Result = LobbyNavigation.Profile;
            _app.RequestStop();
        };

        var news = new Button
        {
            X = Pos.Right(profile) + 2,
            Y = contentTop + 4,
            Text = "_News",
        };
        news.Accepting += (_, e) =>
        {
            e.Handled = true;
            Result = LobbyNavigation.News;
            _app.RequestStop();
        };

        var gallery = new Button
        {
            X = Pos.Right(news) + 2,
            Y = contentTop + 4,
            Text = "_Gallery",
        };
        gallery.Accepting += (_, e) =>
        {
            e.Handled = true;
            Result = LobbyNavigation.Gallery;
            _app.RequestStop();
        };

        var sysopButton = new Button
        {
            X = Pos.Right(gallery) + 2,
            Y = contentTop + 4,
            Text = "_Sysop",
            Visible = user.IsSysop,
            Enabled = user.IsSysop,
        };
        sysopButton.Accepting += (_, e) =>
        {
            e.Handled = true;
            Result = LobbyNavigation.Sysop;
            _app.RequestStop();
        };

        var logout = new Button
        {
            X = user.IsSysop ? Pos.Right(sysopButton) + 2 : Pos.Right(gallery) + 2,
            Y = contentTop + 4,
            Text = "_Logout",
        };
        logout.Accepting += (_, e) =>
        {
            e.Handled = true;
            Result = LobbyNavigation.Logout;
            _app.RequestStop();
        };

        var sysopBadge = new Label
        {
            X = 2,
            Y = contentTop + 7,
            Text = user.IsSysop ? "[ sysop access granted — press S for the console ]" : string.Empty,
        };
        sysopBadge.SetScheme(BbsTheme.Success_);

        Add(artView, welcome, hint, chat, boards, profile, news, gallery, sysopButton, logout, sysopBadge);

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                Result = LobbyNavigation.Logout;
                _app.RequestStop();
                key.Handled = true;
            }
            else if (key == Key.Enter)
            {
                // Enter from anywhere on the lobby jumps into chat — saves a Tab dance.
                Result = LobbyNavigation.Chat;
                _app.RequestStop();
                key.Handled = true;
            }
            else if (key == Key.B || key == Key.B.WithShift)
            {
                Result = LobbyNavigation.Boards;
                _app.RequestStop();
                key.Handled = true;
            }
            else if (key == Key.P || key == Key.P.WithShift)
            {
                Result = LobbyNavigation.Profile;
                _app.RequestStop();
                key.Handled = true;
            }
            else if (key == Key.N || key == Key.N.WithShift)
            {
                Result = LobbyNavigation.News;
                _app.RequestStop();
                key.Handled = true;
            }
            else if (key == Key.G || key == Key.G.WithShift)
            {
                Result = LobbyNavigation.Gallery;
                _app.RequestStop();
                key.Handled = true;
            }
            else if (user.IsSysop && (key == Key.S || key == Key.S.WithShift))
            {
                Result = LobbyNavigation.Sysop;
                _app.RequestStop();
                key.Handled = true;
            }
        };
    }
}
