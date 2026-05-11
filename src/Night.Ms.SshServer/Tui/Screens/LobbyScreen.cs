using Night.Ms.SshServer.Domain;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public enum LobbyNavigation { Chat, Logout }

public sealed class LobbyScreen : Window
{
    private readonly IApplication _app;

    public LobbyScreen(IApplication app, User user, bool justRegistered)
    {
        _app = app;
        Title = $"ssh.night.ms — lobby — {user.Handle}";

        var welcome = new Label
        {
            X = 2,
            Y = 1,
            Text = justRegistered
                ? $"Welcome aboard, {user.Handle}. Your key is bound to this account."
                : $"Welcome back, {user.Handle}.",
        };

        var hint = new Label
        {
            X = 2,
            Y = 3,
            Text = "Choose where to go:",
        };

        var chat = new Button
        {
            X = 2,
            Y = 5,
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
            Y = 5,
            Text = "_Boards (M8)",
        };
        boards.Enabled = false;

        var logout = new Button
        {
            X = Pos.Right(boards) + 2,
            Y = 5,
            Text = "_Logout",
        };
        logout.Accepting += (_, e) =>
        {
            e.Handled = true;
            Result = LobbyNavigation.Logout;
            _app.RequestStop();
        };

        var sysop = new Label
        {
            X = 2,
            Y = 8,
            Text = user.IsSysop ? "[ sysop access granted ]" : string.Empty,
        };

        Add(welcome, hint, chat, boards, logout, sysop);

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
        };
    }
}
