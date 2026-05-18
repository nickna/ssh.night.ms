using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Doors;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Hub for door games. Lists every IDoorGame registered in DI, shows the user's wallet, and
// launches the selected game. Each launch creates a fresh DI scope so the game's ledger and
// rng share a context lifetime with its UI loop — matches the per-screen scope pattern used
// by ChatScreen / NewsScreen / etc.
//
// Number keys 1-9 launch the matching entry; L jumps to leaderboards (added in M6).
internal sealed class DoorsScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;
    private readonly IReadOnlyList<IDoorGame> _games;

    private readonly Label _walletLabel;

    public DoorsScreen(IApplication app, IServiceProvider services, User user)
        : base(app, services, user)
    {
        _app = app;
        _services = services;
        _user = user;
        _games = services.GetServices<IDoorGame>().OrderBy(g => g.Title).ToList();

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

        // Each game gets two rows: a header line with hotkey + title + bet range, then a
        // muted description line below it. Two rows per entry keeps the doors menu skimmable
        // even as the game catalog grows.
        var listY = 4;
        var rowsPerEntry = 2;
        for (var i = 0; i < _games.Count; i++)
        {
            var g = _games[i];
            var titleLine = new Label
            {
                X = 2, Y = listY + i * rowsPerEntry,
                Text = $"  {i + 1}. {g.Title}    (bet {g.MinBet}-{g.MaxBet})",
            };
            var descLine = new Label
            {
                X = 2, Y = listY + i * rowsPerEntry + 1,
                Width = Dim.Fill(2),
                Text = "       " + g.Description,
            };
            descLine.SetScheme(BbsTheme.Hint);
            Add(titleLine, descLine);
        }

        var leaderboardsLine = new Label
        {
            X = 2, Y = listY + _games.Count * rowsPerEntry + 1,
            Text = "  L. Leaderboards",
        };
        Add(leaderboardsLine);

        var hint = new Label
        {
            X = 2, Y = Pos.AnchorEnd(3),
            Width = Dim.Fill(2),
            Text = "[1-9] launch    [L] leaderboards    [Esc] back to lobby",
        };
        hint.SetScheme(BbsTheme.Hint);
        Add(hint);

        KeyDown += OnKey;
        InstallEscapeHandler();

        LoadWalletAsync().FireAndLog(services, nameof(LoadWalletAsync));
    }

    private void OnKey(object? _, Key key)
    {
        if (key.Matches(Key.L))
        {
            key.Handled = true;
            _app.Run(new LeaderboardScreen(_app, _services, _user));
            return;
        }

        for (var i = 0; i < _games.Count && i < 9; i++)
        {
            if (key == DigitKey(i + 1))
            {
                key.Handled = true;
                Launch(_games[i]);
                return;
            }
        }
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
