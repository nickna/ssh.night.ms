using Night.Ms.SshServer.Domain;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Placeholder lobby — M7/M8 wire the actual Chat / Boards / Profile screens. For M5 it's
// enough to prove that the post-auth handoff lands the right handle in the right place.
public sealed class LobbyScreen : Window
{
    public LobbyScreen(User user, bool justRegistered)
    {
        Title = $"ssh.night.ms — lobby — {user.Handle}";

        var welcome = new Label
        {
            X = 2,
            Y = 1,
            Text = justRegistered
                ? $"Welcome aboard, {user.Handle}. Your key is bound to this account."
                : $"Welcome back, {user.Handle}.",
        };

        var menu = new Label
        {
            X = 2,
            Y = 3,
            Text = """
                Chat       — coming in M7
                Boards     — coming in M8
                Profile    — coming later
                Logout     — press [Esc]
                """,
        };

        var sysop = new Label
        {
            X = 2,
            Y = 9,
            Text = user.IsSysop ? "[ sysop access granted ]" : string.Empty,
        };

        Add(welcome, menu, sysop);

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                Application.RequestStop();
                key.Handled = true;
            }
        };
    }
}
