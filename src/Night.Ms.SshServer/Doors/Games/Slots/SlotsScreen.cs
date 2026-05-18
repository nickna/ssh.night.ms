using System.Text;
using System.Text.Json;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Doors.Games.Slots;

internal sealed class SlotsScreen : BbsWindow
{
    private const int BetStep = 5;

    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;
    private readonly SlotsGame _game;
    private readonly SlotsEngine _engine;

    private int _bet;
    private bool _spinning;

    private readonly Label _walletLabel;
    private readonly Label _reelLabel;
    private readonly Label _resultLabel;
    private readonly Label _betLabel;

    public SlotsScreen(IApplication app, IServiceProvider services, User user, SlotsGame game)
        : base(app, services, user)
    {
        _app = app;
        _services = services;
        _user = user;
        _game = game;
        _bet = game.MinBet;
        _engine = new SlotsEngine(services.GetRequiredService<IGameRng>());

        Title = $"ssh.night.ms — slot machine — {user.Handle}";

        var header = new Label { X = 2, Y = 0, Width = Dim.Fill(2), Text = "Slot Machine" };
        header.SetScheme(BbsTheme.Header_);

        _walletLabel = new Label { X = 2, Y = 2, Width = Dim.Fill(2), Text = "Loading wallet…" };
        _walletLabel.SetScheme(BbsTheme.Hint);

        // Reel window: three glyphs in boxes, centered horizontally inside the screen.
        // Starts as dashes (the "no spin yet" placeholder) so the screen has something to
        // render before the user hits Enter.
        _reelLabel = new Label
        {
            X = Pos.Center(),
            Y = 5,
            Text = BuildReelArt('-', '-', '-'),
        };

        _resultLabel = new Label
        {
            X = 2,
            Y = 10,
            Width = Dim.Fill(2),
            Text = "Press [Enter] to spin.",
        };

        _betLabel = new Label
        {
            X = 2,
            Y = 12,
            Width = Dim.Fill(2),
            Text = BuildBetText(),
        };

        var hint = new Label
        {
            X = 2,
            Y = Pos.AnchorEnd(3),
            Width = Dim.Fill(2),
            Text = $"[Enter] spin    [+/-] bet {BetStep} more/less    [P] paytable    [Esc] back",
        };
        hint.SetScheme(BbsTheme.Hint);

        Add(header, _walletLabel, _reelLabel, _resultLabel, _betLabel, hint);

        KeyDown += OnKey;
        InstallEscapeHandler();

        LoadWalletAsync().FireAndLog(services, nameof(LoadWalletAsync));
    }

    private void OnKey(object? _, Key key)
    {
        if (_spinning)
        {
            // While a round is in flight, swallow input so a held-down Enter doesn't queue
            // a second spin before the first commits.
            key.Handled = true;
            return;
        }

        if (key == Key.Enter || key == Key.Space)
        {
            key.Handled = true;
            SpinAsync().FireAndLog(_services, nameof(SpinAsync));
            return;
        }

        if (key.Matches(Key.P))
        {
            key.Handled = true;
            ShowPaytable();
            return;
        }

        if (IsPlusKey(key))
        {
            key.Handled = true;
            AdjustBet(+BetStep);
            return;
        }

        if (IsMinusKey(key))
        {
            key.Handled = true;
            AdjustBet(-BetStep);
            return;
        }
    }

    // Match on the produced rune rather than a fixed Key constant: Terminal.Gui doesn't ship
    // Key.Plus/Key.Minus, and Shift+= vs the numpad '+' arrive as different KeyCode values
    // but agree on the rune. '=' is accepted as an alias because typing the unshifted key
    // for '+' on most US layouts feels natural.
    private static bool IsPlusKey(Key key)
    {
        var r = key.AsRune;
        return r == new System.Text.Rune('+') || r == new System.Text.Rune('=');
    }

    private static bool IsMinusKey(Key key)
    {
        var r = key.AsRune;
        return r == new System.Text.Rune('-') || r == new System.Text.Rune('_');
    }

    private void AdjustBet(int delta)
    {
        var next = Math.Clamp(_bet + delta, _game.MinBet, _game.MaxBet);
        if (next == _bet) return;
        _bet = next;
        _betLabel.Text = BuildBetText();
    }

    private async Task SpinAsync()
    {
        _spinning = true;
        try
        {
            // Determine outcome first, then persist via the ledger. If the ledger throws
            // (insufficient funds, transient DB error) the reels never visibly move — the
            // player only sees the result when the bet has actually committed.
            var result = _engine.Spin();
            var payout = result.Payout(_bet);

            var details = JsonSerializer.SerializeToDocument(new
            {
                reels = new[] { result.Reel1.ToString(), result.Reel2.ToString(), result.Reel3.ToString() },
                multiplier = result.Multiplier,
                match = result.MatchLabel,
            });

            var ledger = _services.GetRequiredService<IGameLedger>();
            RoundOutcome outcome;
            try
            {
                outcome = await ledger.PlayRoundAsync(_user.Id, _game.Key, _bet, payout, details, Shutdown);
            }
            catch (InsufficientFundsException ex)
            {
                _app.Invoke(() => MessageBox.Query(
                    _app, "Not enough coins",
                    $"Bet is {ex.Requested}, but you only have {ex.Available}.\nAdjust your bet or wait for the daily reset.",
                    "_OK"));
                return;
            }

            _app.Invoke(() =>
            {
                _reelLabel.Text = BuildReelArt(result.Reel1.Glyph(), result.Reel2.Glyph(), result.Reel3.Glyph());
                _resultLabel.Text = payout > 0
                    ? $"{result.MatchLabel}  —  +{payout} coins (net {payout - _bet:+#;-#;0})"
                    : $"{result.MatchLabel}  —  lost {_bet} coins";
                ApplyWalletSnapshot(outcome.Wallet);
            });
        }
        finally
        {
            _spinning = false;
        }
    }

    private async Task LoadWalletAsync()
    {
        var wallet = _services.GetRequiredService<IWalletService>();
        var snapshot = await wallet.GetAsync(_user.Id, Shutdown);
        _app.Invoke(() => ApplyWalletSnapshot(snapshot));
    }

    private void ApplyWalletSnapshot(WalletSnapshot snapshot)
    {
        _walletLabel.Text =
            $"Wallet — Daily: {snapshot.DailyCredits}/{snapshot.DailyAllotment}    Winnings: {snapshot.WinningsBalance}    Total: {snapshot.Total}";
    }

    private string BuildBetText() =>
        $"Bet: {_bet} coins   (min {_game.MinBet}, max {_game.MaxBet})";

    private static string BuildReelArt(char a, char b, char c)
    {
        // Three side-by-side boxes drawn with ASCII so column widths stay predictable on
        // any SSH client. Each box is 5 cols wide × 3 rows tall.
        var sb = new StringBuilder();
        sb.Append("+---+ +---+ +---+\n");
        sb.Append($"| {a} | | {b} | | {c} |\n");
        sb.Append("+---+ +---+ +---+");
        return sb.ToString();
    }

    private void ShowPaytable()
    {
        var sb = new StringBuilder();
        sb.AppendLine("Three of a kind:");
        foreach (var (sym, mult) in SlotPaytable.ThreeOfAKindMultipliers)
        {
            sb.AppendLine($"  {sym.DisplayName(),-8} {sym.Glyph()} {sym.Glyph()} {sym.Glyph()}    {mult,4}×");
        }
        sb.AppendLine();
        sb.AppendLine($"Two cherries (any positions):       {SlotPaytable.TwoCherryMultiplier}×");
        sb.AppendLine($"One cherry on reel 1 only:          {SlotPaytable.OneCherryReelOneMultiplier}× (break-even)");

        MessageBox.Query(_app, "Paytable", sb.ToString(), "_OK");
    }
}
