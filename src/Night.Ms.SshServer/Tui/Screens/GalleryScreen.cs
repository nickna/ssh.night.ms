using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Curated art browser. Lists .ans files from the configured directory and renders the
// selected piece via AnsiArtView. Navigation: arrows / h-l for prev/next (wrap),
// 1-9 for direct jump, Enter to re-list (so sysop drops appear without restart),
// Q/Esc to return to the lobby.
internal sealed class GalleryScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly IArtGalleryProvider _gallery;

    private readonly Label _title;
    private readonly AnsiArtView _artView;
    private readonly Label _hint;

    private IReadOnlyList<ArtGalleryEntry> _entries;
    private int _index;

    public GalleryScreen(IApplication app, IServiceProvider services, User user, IArtGalleryProvider gallery)
        : base(app, services, user)
    {
        _app = app;
        _gallery = gallery;
        _entries = gallery.List();
        _index = 0;

        Title = "ssh.night.ms — gallery";

        _title = new Label
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Text = string.Empty,
        };
        _title.SetScheme(BbsTheme.Header_);

        _artView = new AnsiArtView { X = 0, Y = 2 };

        _hint = new Label
        {
            X = 0,
            Y = Pos.AnchorEnd(2),
            Width = Dim.Fill(),
            Text = string.Empty,
        };
        _hint.SetScheme(BbsTheme.Hint);

        Add(_title, _artView, _hint);

        RefreshAndShow();

        KeyDown += OnKey;
    }

    private void OnKey(object? _, Key key)
    {
        // Always allow exit, even from the empty state.
        if (key == Key.Esc || key == Key.Q || key == Key.Q.WithShift)
        {
            _app.RequestStop();
            key.Handled = true;
            return;
        }

        if (key == Key.Enter)
        {
            RefreshAndShow();
            key.Handled = true;
            return;
        }

        if (_entries.Count == 0) return; // remaining bindings need a non-empty gallery

        if (key == Key.CursorLeft || key == Key.H || key == Key.H.WithShift)
        {
            _index = (_index - 1 + _entries.Count) % _entries.Count;
            ShowCurrent();
            key.Handled = true;
            return;
        }

        if (key == Key.CursorRight || key == Key.L || key == Key.L.WithShift)
        {
            _index = (_index + 1) % _entries.Count;
            ShowCurrent();
            key.Handled = true;
            return;
        }

        // Digit keys 1-9 jump to that index (1-based). Inaccessible for 10+ — use arrows.
        for (var d = 1; d <= 9; d++)
        {
            if (key == DigitKey(d))
            {
                var target = d - 1;
                if (target < _entries.Count)
                {
                    _index = target;
                    ShowCurrent();
                }
                key.Handled = true;
                return;
            }
        }
    }

    private static Key DigitKey(int d) => d switch
    {
        1 => Key.D1,
        2 => Key.D2,
        3 => Key.D3,
        4 => Key.D4,
        5 => Key.D5,
        6 => Key.D6,
        7 => Key.D7,
        8 => Key.D8,
        9 => Key.D9,
        _ => Key.Empty,
    };

    private void RefreshAndShow()
    {
        _entries = _gallery.List();
        if (_entries.Count == 0)
        {
            ShowEmptyState();
            return;
        }
        if (_index >= _entries.Count) _index = _entries.Count - 1;
        ShowCurrent();
    }

    private void ShowCurrent()
    {
        var entry = _entries[_index];
        _title.Text = $"{entry.Title}  ({_index + 1} of {_entries.Count})";
        _artView.Grid = _gallery.Load(entry.Id);
        _hint.Text = _entries.Count == 1
            ? "[Enter] refresh    [Esc/Q] back to lobby"
            : "[←/→] prev/next    [1-9] jump    [Enter] refresh    [Esc/Q] back to lobby";
    }

    private void ShowEmptyState()
    {
        _title.Text = "(no art in the gallery yet)";
        _artView.Grid = null;
        _hint.Text = "Sysop: drop .ans files into the configured NIGHTMS_ART_DIR, then press [Enter] to refresh.    [Esc/Q] back to lobby";
    }
}
