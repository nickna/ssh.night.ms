using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// M4 proof-of-life screen: a styled window with a fingerprint readout and a "Logout" button.
// M5+ replaces this with the real Login / Register / Lobby screens.
public sealed class HelloScreen : Window
{
    public HelloScreen(string handle, string algorithm, string fingerprint)
    {
        Title = "ssh.night.ms — M4 driver check";

        Add(
            new Label
            {
                X = 2,
                Y = 1,
                Text = $"Hello, {handle}!",
            },
            new Label
            {
                X = 2,
                Y = 3,
                Text = $"key  {algorithm}\nfp   {fingerprint}",
            },
            new Label
            {
                X = 2,
                Y = 7,
                Text = "Press [Esc] or click Logout to disconnect.",
            });

        var logout = new Button
        {
            X = 2,
            Y = 9,
            Text = "Logout",
            IsDefault = true,
        };
        logout.Accepting += (_, e) =>
        {
            e.Handled = true;
            Application.RequestStop();
        };
        Add(logout);

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
