using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Doors;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Hub for door games. Renders every IDoorGame registered in DI as a card in the same carousel
// the main lobby uses, plus a Leaderboards entry. Each launch creates a fresh DI scope so the
// game's ledger and rng share a context lifetime with its UI loop — matches the per-screen
// scope pattern used by ChatScreen / NewsScreen / etc.
internal sealed class DoorsScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;

    private readonly Label _walletLabel;
    private readonly Label _descLabel;
    private readonly IReadOnlyList<string> _descriptions;

    public DoorsScreen(IApplication app, IServiceProvider services, User user)
        : base(app, services, user)
    {
        _app = app;
        _services = services;
        _user = user;
        var games = services.GetServices<IDoorGame>().OrderBy(g => g.Title).ToList();
        var icons = services.GetRequiredService<ILobbyIconProvider>();

        Title = $"ssh.night.ms — doors — {user.Handle}";

        var header = new Label
        {
            X = 2, Y = 0, Width = Dim.Fill(2),
            Text = "Door Games",
        };
        header.SetScheme(BbsTheme.Header_);

        _walletLabel = new Label
        {
            X = 2, Y = 2, Width = Dim.Fill(2),
            Text = "Loading wallet…",
        };
        _walletLabel.SetScheme(BbsTheme.Hint);

        Add(header, _walletLabel);

        var entries = new List<LobbyCarouselView<Action>.Entry>();
        var descriptions = new List<string>();
        for (var i = 0; i < games.Count && i < 9; i++)
        {
            var game = games[i];
            entries.Add(new LobbyCarouselView<Action>.Entry(
                game.Title,
                DigitKey(i + 1),
                () => Launch(game),
                icons.Get(game.Key)));
            descriptions.Add($"{game.Description}  (bet {game.MinBet}-{game.MaxBet})");
        }
        entries.Add(new LobbyCarouselView<Action>.Entry(
            "Leaderboards",
            Key.L,
            OpenLeaderboards,
            icons.Get("leaderboards")));
        descriptions.Add("Top single wins, lifetime net, and the last 7 days of hot streaks.");
        _descriptions = descriptions;

        var carouselY = 4;
        var carousel = new LobbyCarouselView<Action>(app, entries)
        {
            X = 0, Y = carouselY, Width = Dim.Fill(),
        };
        carousel.EntryActivated += (_, action) => action();
        Add(carousel);

        _descLabel = new Label
        {
            X = 2,
            Y = carouselY + LobbyCarouselView<Action>.RowHeight + 1,
            Width = Dim.Fill(2),
            Text = _descriptions.Count > 0 ? _descriptions[0] : string.Empty,
        };
        _descLabel.SetScheme(BbsTheme.Hint);
        Add(_descLabel);

        carousel.SelectionChanged += (_, idx) => _descLabel.Text = _descriptions[idx];

        var hint = new Label
        {
            X = 2, Y = Pos.AnchorEnd(2),
            Width = Dim.Fill(2),
            Text = "[←→] navigate    [Enter] open    [Esc] back to lobby",
        };
        hint.SetScheme(BbsTheme.Hint);
        Add(hint);

        KeyDown += (_, key) =>
        {
            foreach (var e in entries)
            {
                if (key == e.Hotkey || key == e.Hotkey.WithShift)
                {
                    key.Handled = true;
                    carousel.TrySelectByHotkey(key);
                    return;
                }
            }
        };

        InstallEscapeHandler();
        carousel.SetFocus();

        LoadWalletAsync().FireAndLog(services, nameof(LoadWalletAsync));
    }

    private void Launch(IDoorGame game)
    {
        // Fresh scope per launch so the game's IGameLedger / IWalletService share an
        // AppDbContext lifetime with its UI loop. Disposed when the screen exits.
        using var scope = _services.CreateScope();
        var screen = game.CreateScreen(_app, scope.ServiceProvider, _user);
        _app.Run(screen);
        // Wallet may have changed during the round — refresh the header on return.
        LoadWalletAsync().FireAndLog(_services, nameof(LoadWalletAsync));
    }

    private void OpenLeaderboards()
    {
        _app.Run(new LeaderboardScreen(_app, _services, _user));
    }

    private async Task LoadWalletAsync()
    {
        await using var scope = _services.CreateAsyncScope();
        var wallet = scope.ServiceProvider.GetRequiredService<IWalletService>();
        var snapshot = await wallet.GetAsync(_user.Id, Shutdown);
        _app.Invoke(() =>
        {
            _walletLabel.Text =
                $"Wallet — Daily: {snapshot.DailyCredits}/{snapshot.DailyAllotment}    Winnings: {snapshot.WinningsBalance}    Total: {snapshot.Total}    (daily resets at 00:00 UTC)";
        });
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
}
