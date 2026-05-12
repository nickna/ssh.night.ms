using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Tiny prompt screen — the lobby's Browser button drops here, the user types a URL,
// and on submit we hand a parsed Uri back as Result so BbsSessionRunner can open a
// ReaderScreen against it. Bare scheme inputs (e.g. "example.com") get an https:// prefix.
public sealed class BrowserPromptScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly TextField _url;
    private readonly Label _status;

    public BrowserPromptScreen(IApplication app, IServiceProvider services, User user)
        : base(app, services, user)
    {
        _app = app;
        Title = "ssh.night.ms — browser — [Enter] open — [Esc] cancel";

        var prompt = new Label
        {
            X = 2,
            Y = 1,
            Text = "Enter a URL (e.g. https://example.com or just example.com):",
        };
        prompt.SetScheme(BbsTheme.Hint);

        _url = new TextField
        {
            X = 2,
            Y = 3,
            Width = Dim.Fill(2),
        };
        _url.SetScheme(BbsTheme.Input);

        _status = new Label
        {
            X = 2,
            Y = 5,
            Width = Dim.Fill(2),
        };
        _status.SetScheme(BbsTheme.Status);

        var open = new Button
        {
            X = 2,
            Y = 7,
            Text = "_Open",
            IsDefault = true,
        };
        open.Accepting += (_, e) =>
        {
            e.Handled = true;
            TrySubmit();
        };

        var cancel = new Button
        {
            X = Pos.Right(open) + 2,
            Y = 7,
            Text = "_Cancel",
        };
        cancel.Accepting += (_, e) =>
        {
            e.Handled = true;
            Result = null;
            _app.RequestStop();
        };

        Add(prompt, _url, _status, open, cancel);
        _url.SetFocus();

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                Result = null;
                _app.RequestStop();
                key.Handled = true;
            }
        };
    }

    private void TrySubmit()
    {
        var raw = (_url.Text ?? string.Empty).Trim();
        if (raw.Length == 0)
        {
            SetError("[!] Enter a URL.");
            return;
        }

        // Bare hostnames default to https — saves the user typing the scheme.
        if (!raw.Contains("://", StringComparison.Ordinal))
        {
            raw = "https://" + raw;
        }

        if (!Uri.TryCreate(raw, UriKind.Absolute, out var uri)
            || (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps))
        {
            SetError("[!] Not a valid http(s) URL.");
            return;
        }

        // Catch the duplicate-scheme typo (`https://https://example.com`) before it makes
        // a 12s DNS round-trip to a host literally named "https". Uri.TryCreate happily
        // parses the broken form because "https" is a valid hostname character-wise.
        if (string.Equals(uri.Host, "http", StringComparison.OrdinalIgnoreCase)
            || string.Equals(uri.Host, "https", StringComparison.OrdinalIgnoreCase))
        {
            SetError("[!] URL looks malformed (duplicated scheme?).");
            return;
        }

        Result = uri;
        _app.RequestStop();
    }

    private void SetError(string text)
    {
        _status.Text = text;
        _status.SetScheme(BbsTheme.Warning);
    }
}
