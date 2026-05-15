using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Small text-input prompt that takes a default label (the canonical location name) and
// returns the user's chosen short label. Caller passes the result to
// UserSavedLocations as a new favorite row. Esc/cancel returns null.
public sealed class SaveFavoritePromptScreen : BbsWindow
{
    private const int MaxLabelLength = 64;

    private readonly IApplication _app;
    private readonly TextField _label;

    public SaveFavoritePromptScreen(IApplication app, IServiceProvider services, User user, string defaultLabel)
        : base(app, services, user)
    {
        _app = app;
        Title = "ssh.night.ms — save favorite — [Enter] save  [Esc] cancel";

        var prompt = new Label
        {
            X = 2,
            Y = 1,
            Text = $"Save the current location as a favorite (≤ {MaxLabelLength} chars):",
        };
        prompt.SetScheme(BbsTheme.Hint);

        _label = new TextField
        {
            X = 2,
            Y = 3,
            Width = Dim.Fill(2),
            Text = Truncate(defaultLabel ?? string.Empty, MaxLabelLength),
        };
        _label.SetScheme(BbsTheme.Input);
        _label.Accepting += (_, e) =>
        {
            e.Handled = true;
            Submit();
        };

        var save = new Button
        {
            X = 2,
            Y = 5,
            Text = "_Save",
            IsDefault = true,
        };
        save.Accepting += (_, e) =>
        {
            e.Handled = true;
            Submit();
        };

        var cancel = new Button
        {
            X = Pos.Right(save) + 2,
            Y = 5,
            Text = "_Cancel",
        };
        cancel.Accepting += (_, e) =>
        {
            e.Handled = true;
            Result = null;
            _app.RequestStop();
        };

        Add(prompt, _label, save, cancel);
        _label.SetFocus();

        InstallEscapeHandler(() => Result = null);
    }

    private void Submit()
    {
        var text = Truncate((_label.Text ?? string.Empty).Trim(), MaxLabelLength);
        Result = string.IsNullOrEmpty(text) ? null : text;
        _app.RequestStop();
    }

    private static string Truncate(string s, int max) => s.Length <= max ? s : s[..max];
}
