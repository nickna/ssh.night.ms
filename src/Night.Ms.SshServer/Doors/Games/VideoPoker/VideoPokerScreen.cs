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

namespace Night.Ms.SshServer.Doors.Games.VideoPoker;

internal sealed class VideoPokerScreen : BbsWindow
{
    private const int BetStep = 5;

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
    private readonly Label _handLabel;
    private readonly Label _resultLabel;
    private readonly Label _betLabel;
    private bool _busy;

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

        _walletLabel = new Label { X = 2, Y = 2, Width = Dim.Fill(2), Text = "Loading wallet…" };
        _walletLabel.SetScheme(BbsTheme.Hint);

        _handLabel = new Label { X = 2, Y = 4, Width = Dim.Fill(2), Height = 6, Text = string.Empty };

        _resultLabel = new Label { X = 2, Y = 11, Width = Dim.Fill(2), Text = "Press [Enter] to deal." };

        _betLabel = new Label { X = 2, Y = 13, Width = Dim.Fill(2), Text = BuildBetText() };

        var hint = new Label
        {
            X = 2,
            Y = Pos.AnchorEnd(3),
            Width = Dim.Fill(2),
            Text = "[Enter] deal/draw    [1-5] hold/unhold    [+/-] bet    [P] paytable    [Esc] back",
        };
        hint.SetScheme(BbsTheme.Hint);

        Add(header, _walletLabel, _handLabel, _resultLabel, _betLabel, hint);

        RenderHand();

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
                    RenderHand();
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
        RenderHand();
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
                RenderHand();
                _resultLabel.Text = payout > 0
                    ? $"{rank.DisplayName()}  —  +{payout} coins (net {payout - _bet:+#;-#;0}).  [Enter] to deal again."
                    : $"{rank.DisplayName()}  —  lost {_bet} coins.  [Enter] to deal again.";
                ApplyWalletSnapshot(outcome.Wallet);
            });
        }
        finally
        {
            _busy = false;
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

    private void RenderHand()
    {
        // Layout (one slot ≈ 7 cols):
        //   "  1   "  +  "+----+ "  +  "| AS | "  +  "+----+ "  +  "HELD   "
        // Cards render with the rank padded to two columns so "10" doesn't shift the
        // following cards by a character.
        var sb = new StringBuilder();

        // Slot index row (positions visually under each card).
        sb.Append("  ");
        for (var i = 0; i < 5; i++) sb.Append($"  {i + 1}    ");
        sb.AppendLine();

        sb.Append("  ");
        for (var i = 0; i < 5; i++) sb.Append("+----+ ");
        sb.AppendLine();

        sb.Append("  ");
        for (var i = 0; i < 5; i++)
        {
            var rank = _hand[i]?.RankLabel ?? "--";
            sb.Append($"| {rank.PadRight(2)} | ");
        }
        sb.AppendLine();

        sb.Append("  ");
        for (var i = 0; i < 5; i++)
        {
            var suit = _hand[i]?.SuitGlyph ?? " ";
            sb.Append($"|  {suit} | ");
        }
        sb.AppendLine();

        sb.Append("  ");
        for (var i = 0; i < 5; i++) sb.Append("+----+ ");

        if (_phase == Phase.AwaitingDraw)
        {
            sb.AppendLine();
            sb.Append("  ");
            for (var i = 0; i < 5; i++) sb.Append(_holds[i] ? " HELD  " : "       ");
        }

        _handLabel.Text = sb.ToString();
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
}
