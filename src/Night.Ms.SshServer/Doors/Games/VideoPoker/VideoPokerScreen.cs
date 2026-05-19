using System.Text;
using System.Text.Json;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Doors.Games.Common;
using Night.Ms.SshServer.Doors.Games.Common.Cards;
using Night.Ms.SshServer.Tui;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Doors.Games.VideoPoker;

internal sealed class VideoPokerScreen : BbsWindow
{
    private const int BetStep = 5;

    // 80ms × ~19 frames ≈ 1.5s of border-flash + coin shower on a paying hand. The flash
    // continues until the last coin has floated off the table. Matches the cadence
    // SlotsScreen uses so both door games feel cohesive.
    private static readonly TimeSpan FlashFrameInterval = TimeSpan.FromMilliseconds(80);
    private const int FlashTotalFrames = 19;

    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;
    private readonly VideoPokerGame _game;
    private readonly IGameRng _rng;

    private enum Phase { Idle, AwaitingDraw, Showdown }
    private Phase _phase = Phase.Idle;

    private int _bet;
    private Deck? _deck;
    private readonly Card?[] _dealt = new Card?[5];     // original deal — preserved for the audit row
    private readonly Card?[] _hand = new Card?[5];      // current visible hand (mutated by Draw)
    private readonly bool[] _holds = new bool[5];

    private readonly Label _walletLabel;
    private readonly VideoPokerTableView _table;
    private readonly Label _resultLabel;
    private readonly Label _betLabel;
    private bool _busy;

    private int _flashFrame;
    private object? _flashTimerToken;
    private bool _disposed;

    public VideoPokerScreen(IApplication app, IServiceProvider services, User user, VideoPokerGame game)
        : base(app, services, user)
    {
        _app = app;
        _services = services;
        _user = user;
        _game = game;
        _rng = services.GetRequiredService<IGameRng>();
        _bet = game.MinBet;

        Title = $"ssh.night.ms — video poker — {user.Handle}";

        var header = new Label { X = 2, Y = 0, Width = Dim.Fill(2), Text = "Video Poker — 9/6 Jacks or Better" };
        header.SetScheme(BbsTheme.Header_);

        _walletLabel = new Label { X = 2, Y = 1, Width = Dim.Fill(2), Text = "Loading wallet…" };
        _walletLabel.SetScheme(BbsTheme.Hint);

        _table = new VideoPokerTableView
        {
            X = Pos.Center(),
            Y = 3,
        };

        _resultLabel = new Label { X = 2, Y = 21, Width = Dim.Fill(2), Text = "Press [Enter] to deal." };

        _betLabel = new Label { X = 2, Y = 22, Width = Dim.Fill(2), Text = BuildBetText() };

        var hint = new Label
        {
            X = 2,
            Y = Pos.AnchorEnd(3),
            Width = Dim.Fill(2),
            Text = "[Enter] deal/draw    [1-5] hold/unhold    [+/-] bet    [P] paytable    [Esc] back",
        };
        hint.SetScheme(BbsTheme.Hint);

        Add(header, _walletLabel, _table, _resultLabel, _betLabel, hint);

        KeyDown += OnKey;
        InstallEscapeHandler();

        LoadWalletAsync().FireAndLog(services, nameof(LoadWalletAsync));
    }

    private void OnKey(object? _, Key key)
    {
        if (_busy)
        {
            key.Handled = true;
            return;
        }

        if (key.Matches(Key.P))
        {
            key.Handled = true;
            ShowPaytable();
            return;
        }

        if (key == Key.Enter || key == Key.Space)
        {
            key.Handled = true;
            if (_phase == Phase.AwaitingDraw)
            {
                DrawAsync().FireAndLog(_services, nameof(DrawAsync));
            }
            else
            {
                Deal();
            }
            return;
        }

        if (_phase == Phase.AwaitingDraw)
        {
            for (var i = 0; i < 5; i++)
            {
                if (key == DigitKey(i + 1))
                {
                    key.Handled = true;
                    _holds[i] = !_holds[i];
                    _table.SetHolds(_holds);
                    return;
                }
            }
            // No bet changes mid-hand: the coin level is committed at deal time.
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

    private void Deal()
    {
        // A new deal cancels any in-flight win flash and clears the persistent showdown
        // highlight from the previous hand so the player gets a clean table.
        CancelFlashTimer();
        _table.ClearWinFlash();
        _table.ClearShowdown();

        _deck = new Deck(_rng);
        for (var i = 0; i < 5; i++)
        {
            var card = _deck.Draw();
            _dealt[i] = card;
            _hand[i] = card;
            _holds[i] = false;
        }
        _phase = Phase.AwaitingDraw;
        _resultLabel.Text = "Pick cards to hold with 1-5, then [Enter] to draw.";
        _table.SetHand(_hand);
        _table.SetHolds(_holds);
    }

    private async Task DrawAsync()
    {
        _busy = true;
        try
        {
            // Replace non-held positions with fresh draws.
            for (var i = 0; i < 5; i++)
            {
                if (!_holds[i]) _hand[i] = _deck!.Draw();
            }

            var finalHand = _hand.Select(c => c!).ToArray();
            var rank = HandEvaluator.Evaluate(finalHand);
            var payout = VideoPokerPaytable.Payout(_bet, rank);

            var details = JsonSerializer.SerializeToDocument(new
            {
                dealt = _dealt.Select(c => c!.ToString()).ToArray(),
                holds = _holds.ToArray(),
                final = finalHand.Select(c => c.ToString()).ToArray(),
                rank = rank.ToString(),
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

            _phase = Phase.Showdown;
            _app.Invoke(() =>
            {
                _table.SetHand(_hand);
                var paid = payout > 0;
                var winningIndices = paid ? WinningCardFinder.Find(finalHand, rank) : Array.Empty<int>();
                _table.SetShowdown(rank, winningIndices, paid);

                _resultLabel.Text = paid
                    ? $"{rank.DisplayName()}  —  +{payout} coins (net {payout - _bet:+#;-#;0}).  [Enter] to deal again."
                    : $"{rank.DisplayName()}  —  lost {_bet} coins.  [Enter] to deal again.";
                ApplyWalletSnapshot(outcome.Wallet);

                if (paid) StartWinFlash(rank, payout);
            });
        }
        finally
        {
            _busy = false;
        }
    }

    private void StartWinFlash(HandRank rank, int payout)
    {
        var tier = rank >= HandRank.StraightFlush ? WinTier.Jackpot : WinTier.Normal;
        var coins = Math.Clamp(payout / Math.Max(_bet, 1), 5, 30);
        _flashFrame = 0;
        _table.SetWinFlash(tier, 0);
        _table.AddCoinBurst(coins);
        _flashTimerToken = _app.AddTimeout(FlashFrameInterval, AdvanceFlashFrame);
    }

    private bool AdvanceFlashFrame()
    {
        if (_disposed) return false;
        _flashFrame++;
        // The showdown rank is held by the table view; we only need the tier for the
        // border-color cycle. Re-derive it from the active showdown each tick so the
        // jackpot/normal distinction stays correct even if the rank somehow shifts.
        var tier = _phase == Phase.Showdown
            ? GetActiveTier()
            : WinTier.Normal;
        _table.SetWinFlash(tier, _flashFrame);
        _table.AdvanceCoinBurst();
        if (_flashFrame >= FlashTotalFrames && !_table.HasCoins)
        {
            _table.ClearWinFlash();
            _flashTimerToken = null;
            return false;
        }
        return true;
    }

    // Look at the most recent evaluated hand to decide the flash tier. The screen doesn't
    // keep an explicit copy of the showdown rank (the table view owns it), so we read it
    // from the final hand we still hold in _hand.
    private WinTier GetActiveTier()
    {
        var finalHand = _hand.Where(c => c is not null).Select(c => c!).ToArray();
        if (finalHand.Length < 5) return WinTier.Normal;
        var rank = HandEvaluator.Evaluate(finalHand);
        return rank >= HandRank.StraightFlush ? WinTier.Jackpot : WinTier.Normal;
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

    private void AdjustBet(int delta)
    {
        var next = Math.Clamp(_bet + delta, _game.MinBet, _game.MaxBet);
        if (next == _bet) return;
        _bet = next;
        _betLabel.Text = BuildBetText();
    }

    private string BuildBetText()
    {
        var coinLevel = _bet / VideoPokerPaytable.CoinSize;
        var coinTag = coinLevel == VideoPokerPaytable.MaxCoinLevel
            ? "max coin — Royal pays 4000"
            : $"{coinLevel} coin{(coinLevel == 1 ? "" : "s")}";
        return $"Bet: {_bet}   ({coinTag})   range {_game.MinBet}-{_game.MaxBet}";
    }

    private void ShowPaytable()
    {
        var sb = new StringBuilder();
        sb.AppendLine("9/6 Jacks-or-Better, per coin (bet 5 = 1 coin):");
        sb.AppendLine();
        sb.AppendLine($"  Royal Flush       250   (max coin: {VideoPokerPaytable.MaxCoinRoyalFlushPayout})");
        sb.AppendLine($"  Straight Flush    {VideoPokerPaytable.PerCoinPayout(HandRank.StraightFlush)}");
        sb.AppendLine($"  Four of a Kind    {VideoPokerPaytable.PerCoinPayout(HandRank.FourOfAKind)}");
        sb.AppendLine($"  Full House        {VideoPokerPaytable.PerCoinPayout(HandRank.FullHouse)}");
        sb.AppendLine($"  Flush             {VideoPokerPaytable.PerCoinPayout(HandRank.Flush)}");
        sb.AppendLine($"  Straight          {VideoPokerPaytable.PerCoinPayout(HandRank.Straight)}");
        sb.AppendLine($"  Three of a Kind   {VideoPokerPaytable.PerCoinPayout(HandRank.ThreeOfAKind)}");
        sb.AppendLine($"  Two Pair          {VideoPokerPaytable.PerCoinPayout(HandRank.TwoPair)}");
        sb.AppendLine($"  Jacks or Better   {VideoPokerPaytable.PerCoinPayout(HandRank.JacksOrBetter)}");

        MessageBox.Query(_app, "Paytable", sb.ToString(), "_OK");
    }

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

    private static Key DigitKey(int d) => d switch
    {
        1 => Key.D1, 2 => Key.D2, 3 => Key.D3, 4 => Key.D4, 5 => Key.D5,
        _ => Key.Empty,
    };

    protected override void Dispose(bool disposing)
    {
        if (disposing && !_disposed)
        {
            _disposed = true;
            CancelFlashTimer();
        }
        base.Dispose(disposing);
    }
}
