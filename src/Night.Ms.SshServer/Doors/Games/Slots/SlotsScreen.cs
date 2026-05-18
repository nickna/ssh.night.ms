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

    // 60ms × 33 frames ≈ 1.98s total spin. Reels lock sequentially at the schedule below so
    // the player gets the classic "click… click… click" cadence (left, middle, right).
    private static readonly TimeSpan SpinFrameInterval = TimeSpan.FromMilliseconds(60);
    private const int Reel1LockFrame = 18;
    private const int Reel2LockFrame = 24;
    private const int Reel3LockFrame = 33;

    // 80ms × ~19 frames ≈ 1.5s of border-flash + coin shower on a paying spin. Continues
    // until the last coin has floated off the cabinet.
    private static readonly TimeSpan FlashFrameInterval = TimeSpan.FromMilliseconds(80);
    private const int FlashTotalFrames = 19;

    // Multiplier threshold that distinguishes "jackpot" win flash (faster red↔gold cycle)
    // from a normal win flash (slower gold↔white cycle). 200× covers Bar and Seven 3-of-a-
    // kind — the two top-tier symbols in SlotPaytable.
    private const int JackpotMultiplierThreshold = 200;

    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;
    private readonly SlotsGame _game;
    private readonly SlotsEngine _engine;

    private int _bet;
    private bool _spinning;
    private int _spinFrame;
    private int _flashFrame;
    private SlotsResult? _pendingResult;
    private WalletSnapshot? _pendingWallet;
    private int _pendingPayout;
    private object? _spinTimerToken;
    private object? _flashTimerToken;
    private bool _disposed;

    private readonly Label _walletLabel;
    private readonly SlotsCabinetView _cabinet;
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

        _walletLabel = new Label { X = 2, Y = 1, Width = Dim.Fill(2), Text = "Loading wallet…" };
        _walletLabel.SetScheme(BbsTheme.Hint);

        // Cabinet sits at the top of the interior; 38 cols wide, 13 rows tall, centered.
        _cabinet = new SlotsCabinetView
        {
            X = Pos.Center(),
            Y = 3,
        };

        _resultLabel = new Label
        {
            X = 2,
            Y = 17,
            Width = Dim.Fill(2),
            Text = "Press [Enter] to spin.",
        };

        _betLabel = new Label
        {
            X = 2,
            Y = 18,
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

        Add(header, _walletLabel, _cabinet, _resultLabel, _betLabel, hint);

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
        // `_spinning` is set synchronously (before the first await) so a second key press
        // arriving while the await is in flight gets swallowed by OnKey.
        _spinning = true;
        try
        {
            // Determine outcome first, then persist via the ledger. If the ledger throws
            // (insufficient funds, transient DB error) the reels never visibly move — the
            // animation only starts once the bet has actually committed.
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
                _spinning = false;
                return;
            }

            _pendingResult = result;
            _pendingWallet = outcome.Wallet;
            _pendingPayout = payout;

            _app.Invoke(StartSpinAnimation);
        }
        catch
        {
            _spinning = false;
            throw;
        }
    }

    private void StartSpinAnimation()
    {
        // A new spin always cancels any in-flight win flash from the previous round so the
        // border and coin tray reset before the reels start scrolling.
        CancelFlashTimer();
        _cabinet.ClearWinFlash();

        _resultLabel.Text = "Spinning…";
        _spinFrame = 0;
        _cabinet.StartSpinning(0);
        _cabinet.StartSpinning(1);
        _cabinet.StartSpinning(2);

        _spinTimerToken = _app.AddTimeout(SpinFrameInterval, AdvanceSpinFrame);
    }

    private bool AdvanceSpinFrame()
    {
        if (_disposed) return false;
        _spinFrame++;

        if (_spinFrame == Reel1LockFrame)
        {
            _cabinet.LockReel(0, _pendingResult!.Reel1);
        }
        else if (_spinFrame == Reel2LockFrame)
        {
            _cabinet.LockReel(1, _pendingResult!.Reel2);
        }
        else if (_spinFrame >= Reel3LockFrame)
        {
            _cabinet.LockReel(2, _pendingResult!.Reel3);
            FinishSpin();
            _spinTimerToken = null;
            return false;
        }
        else
        {
            _cabinet.AdvanceSpin();
        }

        return true;
    }

    private void FinishSpin()
    {
        var result = _pendingResult!;
        var payout = _pendingPayout;

        _resultLabel.Text = payout > 0
            ? $"{result.MatchLabel}  —  +{payout} coins (net {payout - _bet:+#;-#;0})"
            : $"{result.MatchLabel}  —  lost {_bet} coins";
        if (_pendingWallet is { } wallet) ApplyWalletSnapshot(wallet);

        // Release the input lock before the win flash plays so the player can immediately
        // re-spin. The flash is purely decorative; a new spin will cancel it.
        _spinning = false;

        if (payout > 0)
        {
            var tier = result.Multiplier >= JackpotMultiplierThreshold ? WinTier.Jackpot : WinTier.Normal;
            var coins = Math.Clamp(payout / Math.Max(_bet, 1), 5, 20);
            _flashFrame = 0;
            _cabinet.SetWinFlash(tier, 0);
            _cabinet.AddCoinBurst(coins);
            _flashTimerToken = _app.AddTimeout(FlashFrameInterval, AdvanceFlashFrame);
        }
    }

    private bool AdvanceFlashFrame()
    {
        if (_disposed) return false;
        _flashFrame++;
        var tier = (_pendingResult?.Multiplier ?? 0) >= JackpotMultiplierThreshold
            ? WinTier.Jackpot
            : WinTier.Normal;
        _cabinet.SetWinFlash(tier, _flashFrame);
        _cabinet.AdvanceCoinBurst();
        if (_flashFrame >= FlashTotalFrames && !_cabinet.HasCoins)
        {
            _cabinet.ClearWinFlash();
            _flashTimerToken = null;
            return false;
        }
        return true;
    }

    private void CancelFlashTimer()
    {
        if (_flashTimerToken is not null)
        {
            try { _app.RemoveTimeout(_flashTimerToken); } catch { /* ignore */ }
            _flashTimerToken = null;
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

    protected override void Dispose(bool disposing)
    {
        if (disposing && !_disposed)
        {
            _disposed = true;
            if (_spinTimerToken is not null)
            {
                try { _app.RemoveTimeout(_spinTimerToken); } catch { /* ignore */ }
                _spinTimerToken = null;
            }
            if (_flashTimerToken is not null)
            {
                try { _app.RemoveTimeout(_flashTimerToken); } catch { /* ignore */ }
                _flashTimerToken = null;
            }
        }
        base.Dispose(disposing);
    }
}
