using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Providers.Finance;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Result of the add/edit prompt. Caller persists this as a UserWatchlistItem row.
public sealed record AddWatchlistItemResult(WatchlistKind Kind, string Symbol, string Canonical);

// Single-text-field prompt that takes a raw symbol the user types and routes it through
// SymbolResolver to figure out (Kind, Canonical). A preview label updates on every keystroke
// so the user sees "BTC → bitcoin (crypto)" before pressing Save.
//
// Two flows:
//   - Add: empty default, OK enabled only when SymbolResolver returns non-null.
//   - Edit: defaultSymbol pre-fills the field; saving still re-resolves so the user can
//     re-categorize by typing a prefix.
//
// Returns AddWatchlistItemResult on save, null on cancel/Esc.
public sealed class AddWatchlistItemPromptScreen : BbsWindow
{
    private const int MaxSymbolLength = 32;

    private readonly IApplication _app;
    private readonly TextField _input;
    private readonly Label _preview;
    private readonly Button _save;

    public AddWatchlistItemPromptScreen(IApplication app, IServiceProvider services, User user, string? defaultSymbol = null)
        : base(app, services, user)
    {
        _app = app;
        Title = defaultSymbol is null
            ? "ssh.night.ms — add watchlist symbol — [Enter] save  [Esc] cancel"
            : "ssh.night.ms — edit watchlist symbol — [Enter] save  [Esc] cancel";

        var prompt = new Label
        {
            X = 2,
            Y = 1,
            Width = Dim.Fill(2),
            Text = "Symbol (auto-detect):  AAPL  ·  BTC  ·  EUR/USD   — use s:/c:/fx: prefix to force a kind",
        };
        prompt.SetScheme(BbsTheme.Hint);

        _input = new TextField
        {
            X = 2,
            Y = 3,
            Width = Dim.Fill(2),
            Text = Truncate(defaultSymbol ?? string.Empty, MaxSymbolLength),
        };
        _input.SetScheme(BbsTheme.Input);
        _input.TextChanged += (_, _) => UpdatePreview();
        _input.Accepting += (_, e) =>
        {
            e.Handled = true;
            Submit();
        };

        _preview = new Label
        {
            X = 2,
            Y = 5,
            Width = Dim.Fill(2),
            Text = "(type a symbol)",
        };
        _preview.SetScheme(BbsTheme.Status);

        _save = new Button
        {
            X = 2,
            Y = 7,
            Text = "_Save",
            IsDefault = true,
        };
        _save.Accepting += (_, e) =>
        {
            e.Handled = true;
            Submit();
        };

        var cancel = new Button
        {
            X = Pos.Right(_save) + 2,
            Y = 7,
            Text = "_Cancel",
        };
        cancel.Accepting += (_, e) =>
        {
            e.Handled = true;
            Result = null;
            _app.RequestStop();
        };

        Add(prompt, _input, _preview, _save, cancel);
        _input.SetFocus();
        UpdatePreview();

        InstallEscapeHandler(() => Result = null);
    }

    private void UpdatePreview()
    {
        var raw = _input.Text ?? string.Empty;
        var resolved = SymbolResolver.Resolve(raw);
        if (resolved is null)
        {
            _preview.Text = string.IsNullOrWhiteSpace(raw)
                ? "(type a symbol)"
                : $"can't parse '{raw}'.  Examples: AAPL, BTC, EUR/USD";
            _preview.SetScheme(BbsTheme.Warning);
        }
        else
        {
            _preview.Text = $"{raw.Trim()}  →  {resolved.DisplayHint}";
            _preview.SetScheme(BbsTheme.Success_);
        }
    }

    private void Submit()
    {
        var raw = Truncate((_input.Text ?? string.Empty).Trim(), MaxSymbolLength);
        var resolved = SymbolResolver.Resolve(raw);
        if (resolved is null)
        {
            _preview.Text = string.IsNullOrEmpty(raw) ? "(type a symbol)" : $"can't parse '{raw}'.";
            _preview.SetScheme(BbsTheme.Warning);
            _input.SetFocus();
            return;
        }
        Result = new AddWatchlistItemResult(resolved.Kind, raw, resolved.Canonical);
        _app.RequestStop();
    }

    private static string Truncate(string s, int max) => s.Length <= max ? s : s[..max];
}
