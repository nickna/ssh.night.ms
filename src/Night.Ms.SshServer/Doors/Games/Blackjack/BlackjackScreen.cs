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

namespace Night.Ms.SshServer.Doors.Games.Blackjack;

internal sealed class BlackjackScreen : BbsWindow
{
    private const int BetStep = 10;

    private static readonly TimeSpan FlashFrameInterval = TimeSpan.FromMilliseconds(80);
    private const int FlashTotalFrames = 19;

    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;
    private readonly BlackjackGame _game;
    private readonly IGameRng _rng;

    private int _bet;
    private BlackjackGameState? _state;
    private bool _busy;
    private bool _committed;          // true once the current round was written to the ledger.
    private long _walletTotal;        // latest wallet snapshot total — used to gate Double/Split affordability.

    private readonly Label _walletLabel;
    private readonly Label _betLabel;
    private readonly Label _resultLabel;
    private readonly Label _hintLabel;
    private readonly BlackjackTableView _table;

    private int _flashFrame;
    private object? _flashTimerToken;
    private bool _disposed;

    public BlackjackScreen(IApplication app, IServiceProvider services, User user, BlackjackGame game)
        : base(app, services, user)
    {
        _app = app;
        _services = services;
        _user = user;
        _game = game;
        _rng = services.GetRequiredService<IGameRng>();
        _bet = game.MinBet;

        Title = $"ssh.night.ms — blackjack — {user.Handle}";

        var header = new Label { X = 2, Y = 0, Width = Dim.Fill(2), Text = "Blackjack — dealer stands on soft 17, BJ pays 3:2" };
        header.SetScheme(BbsTheme.Header_);

        _walletLabel = new Label { X = 2, Y = 1, Width = Dim.Fill(2), Text = "Loading wallet…" };
        _walletLabel.SetScheme(BbsTheme.Hint);

        _table = new BlackjackTableView
        {
            X = Pos.Center(),
            Y = 3,
        };

        _resultLabel = new Label { X = 2, Y = 21, Width = Dim.Fill(2), Text = "Press [Enter] to deal." };
        _betLabel = new Label { X = 2, Y = 22, Width = Dim.Fill(2), Text = BuildBetText() };
        _hintLabel = new Label { X = 2, Y = Pos.AnchorEnd(3), Width = Dim.Fill(2), Text = BuildIdleHint() };
        _hintLabel.SetScheme(BbsTheme.Hint);

        Add(header, _walletLabel, _table, _resultLabel, _betLabel, _hintLabel);

        KeyDown += OnKey;

        LoadWalletAsync().FireAndLog(services, nameof(LoadWalletAsync));
    }

    private bool IsPlaying => _state is { HandComplete: false };
    private bool IsBetween => _state is null || _state.HandComplete;

    private void OnKey(object? _, Key key)
    {
        if (_busy)
        {
            key.Handled = true;
            return;
        }

        // ESC: between rounds, exit cleanly; mid-round, settle (auto-stand) and commit first.
        if (key == Key.Esc)
        {
            key.Handled = true;
            if (IsPlaying)
            {
                AbortAndExitAsync().FireAndLog(_services, nameof(AbortAndExitAsync));
            }
            else
            {
                _app.RequestStop();
            }
            return;
        }

        if (key.Matches(Key.R) || key.AsRune == new System.Text.Rune('?'))
        {
            key.Handled = true;
            ShowRules();
            return;
        }

        if (IsBetween)
        {
            if (key == Key.Enter || key == Key.Space)
            {
                key.Handled = true;
                DealAsync().FireAndLog(_services, nameof(DealAsync));
                return;
            }
            if (IsPlusKey(key)) { key.Handled = true; AdjustBet(+BetStep); return; }
            if (IsMinusKey(key)) { key.Handled = true; AdjustBet(-BetStep); return; }
            return;
        }

        // Mid-round: action keys.
        var legal = BlackjackEngine.LegalActions(_state!, AvailableForActions());

        if (key.Matches(Key.H) && legal.Contains(BlackjackAction.Hit))
        {
            key.Handled = true;
            ApplyAndRefresh(BlackjackAction.Hit);
            return;
        }
        if (key.Matches(Key.S) && legal.Contains(BlackjackAction.Stand))
        {
            key.Handled = true;
            ApplyAndRefresh(BlackjackAction.Stand);
            return;
        }
        if (key.Matches(Key.D) && legal.Contains(BlackjackAction.Double))
        {
            key.Handled = true;
            ApplyAndRefresh(BlackjackAction.Double);
            return;
        }
        if (key.Matches(Key.P) && legal.Contains(BlackjackAction.Split))
        {
            key.Handled = true;
            ApplyAndRefresh(BlackjackAction.Split);
            return;
        }
    }

    private void ApplyAndRefresh(BlackjackAction action)
    {
        BlackjackEngine.ApplyAction(_state!, action);
        _table.SetState(_state);
        if (_state!.HandComplete)
        {
            CommitRoundAsync().FireAndLog(_services, nameof(CommitRoundAsync));
        }
        else
        {
            _resultLabel.Text = BuildInPlayResultText();
            _hintLabel.Text = BuildPlayingHint();
        }
    }

    private async Task DealAsync()
    {
        _busy = true;
        try
        {
            // The full wager (initial bet) must fit in the wallet up-front. Double/Split
            // increments are gated separately by AvailableForActions().
            if (_bet > _walletTotal)
            {
                MessageBox.Query(_app, "Not enough coins",
                    $"Bet is {_bet}, but you only have {_walletTotal}.\nLower your bet or wait for the daily reset.",
                    "_OK");
                return;
            }

            CancelFlashTimer();
            _table.ClearWinFlash();

            var deck = new Deck(_rng);
            _state = BlackjackEngine.DealInitial(deck, _bet);
            _committed = false;
            _table.SetState(_state);

            if (_state.HandComplete)
            {
                // Natural blackjack or dealer BJ — settle immediately, no actions for the player.
                await CommitRoundAsync();
            }
            else
            {
                _app.Invoke(() =>
                {
                    _resultLabel.Text = BuildInPlayResultText();
                    _hintLabel.Text = BuildPlayingHint();
                });
            }
        }
        finally
        {
            _busy = false;
        }
    }

    // For mid-hand disconnects/ESC: stand every unresolved hand, play dealer, settle, commit.
    private async Task AbortAndExitAsync()
    {
        _busy = true;
        try
        {
            if (_state is not null && !_state.HandComplete)
            {
                foreach (var hand in _state.PlayerHands)
                {
                    if (!hand.Resolved) hand.Resolved = true;
                }
                _state.ActiveIndex = _state.PlayerHands.Count;
                BlackjackEngine.PlayDealer(_state);
                BlackjackEngine.Settle(_state);
                _state.HandComplete = true;
                await CommitRoundAsync();
            }
        }
        finally
        {
            _busy = false;
            _app.Invoke(() => _app.RequestStop());
        }
    }

    private async Task CommitRoundAsync()
    {
        if (_committed || _state is null) return;
        _committed = true;

        var totalBet = _state.TotalBet;
        var totalPayout = _state.TotalPayout;
        var details = SerializeRoundDetails(_state);

        var ledger = _services.GetRequiredService<IGameLedger>();
        RoundOutcome outcome;
        try
        {
            outcome = await ledger.PlayRoundAsync(_user.Id, _game.Key, totalBet, totalPayout, details, Shutdown);
        }
        catch (InsufficientFundsException ex)
        {
            // Should not happen for the initial bet (gated in DealAsync) — if it does, it
            // means the wallet was drained in another session between deal and settle.
            _app.Invoke(() => MessageBox.Query(_app, "Not enough coins",
                $"Bet was {ex.Requested}, but only {ex.Available} available at settle time.",
                "_OK"));
            return;
        }

        _app.Invoke(() =>
        {
            _table.SetState(_state);
            _resultLabel.Text = BuildSettledResultText();
            _hintLabel.Text = BuildIdleHint();
            ApplyWalletSnapshot(outcome.Wallet);

            var net = totalPayout - totalBet;
            if (net > 0) StartWinFlash(net, totalBet);
        });
    }

    private static JsonDocument SerializeRoundDetails(BlackjackGameState state)
    {
        return JsonSerializer.SerializeToDocument(new
        {
            dealer = state.Dealer.Select(c => c.ToString()).ToArray(),
            hands = state.PlayerHands.Select(h => new
            {
                cards = h.Cards.Select(c => c.ToString()).ToArray(),
                bet = h.Bet,
                doubled = h.Doubled,
                fromSplit = h.FromSplit,
                fromSplitAces = h.FromSplitAces,
                result = h.Result?.ToString(),
                payout = h.Payout,
            }).ToArray(),
        });
    }

    private void StartWinFlash(int net, int totalBet)
    {
        // Net-positive only — pushes don't flash. Jackpot tier for >= 1× bet net win
        // (i.e. anything 2:1 or 3:2 on a big bet); normal tier otherwise.
        var tier = net >= totalBet ? WinTier.Jackpot : WinTier.Normal;
        var coins = Math.Clamp(net / 5, 5, 30);
        _flashFrame = 0;
        _table.SetWinFlash(tier, 0);
        _table.AddCoinBurst(coins);
        _flashTimerToken = _app.AddTimeout(FlashFrameInterval, AdvanceFlashFrame);
    }

    private bool AdvanceFlashFrame()
    {
        if (_disposed) return false;
        _flashFrame++;
        _table.SetWinFlash(_state?.TotalPayout - _state?.TotalBet >= _state?.TotalBet ? WinTier.Jackpot : WinTier.Normal, _flashFrame);
        _table.AdvanceCoinBurst();
        if (_flashFrame >= FlashTotalFrames && !_table.HasCoins)
        {
            _table.ClearWinFlash();
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
        _walletTotal = snapshot.Total;
        _walletLabel.Text =
            $"Wallet — Daily: {snapshot.DailyCredits}/{snapshot.DailyAllotment}    Winnings: {snapshot.WinningsBalance}    Total: {snapshot.Total}";
    }

    // Coins still available to wager on Double/Split actions, given what's already
    // committed to the table for this round. The initial bet is "soft-debited" in this
    // accounting since the ledger commit at hand-end will charge total bet.
    private long AvailableForActions()
    {
        var committedToTable = _state?.TotalBet ?? 0;
        return Math.Max(0, _walletTotal - committedToTable);
    }

    private void AdjustBet(int delta)
    {
        var next = Math.Clamp(_bet + delta, _game.MinBet, _game.MaxBet);
        if (next == _bet) return;
        _bet = next;
        _betLabel.Text = BuildBetText();
    }

    private string BuildBetText() => $"Bet: {_bet}   range {_game.MinBet}-{_game.MaxBet}";

    private string BuildIdleHint() =>
        "[Enter] deal    [+/-] bet    [R] rules    [Esc] back";

    private string BuildPlayingHint()
    {
        if (_state is null) return BuildIdleHint();
        var legal = BlackjackEngine.LegalActions(_state, AvailableForActions());
        var sb = new StringBuilder();
        if (legal.Contains(BlackjackAction.Hit)) sb.Append("[H] hit    ");
        if (legal.Contains(BlackjackAction.Stand)) sb.Append("[S] stand    ");
        if (legal.Contains(BlackjackAction.Double)) sb.Append("[D] double    ");
        if (legal.Contains(BlackjackAction.Split)) sb.Append("[P] split    ");
        sb.Append("[Esc] back");
        return sb.ToString();
    }

    private string BuildInPlayResultText()
    {
        if (_state is null) return string.Empty;
        if (_state.PlayerHands.Count == 1)
            return "Choose your action.";
        return $"Playing HAND {_state.ActiveIndex + 1}.";
    }

    private string BuildSettledResultText()
    {
        if (_state is null) return string.Empty;
        var net = _state.TotalPayout - _state.TotalBet;
        var verdicts = string.Join(" / ", _state.PlayerHands.Select(h => h.Result?.DisplayName() ?? "—"));
        var netTag = net switch
        {
            > 0 => $"+{net}",
            0 => "even",
            _ => net.ToString(),
        };
        return $"{verdicts}  —  net {netTag} coins.  [Enter] to deal again.";
    }

    private void ShowRules()
    {
        var sb = new StringBuilder();
        sb.AppendLine("Blackjack — house rules:");
        sb.AppendLine();
        sb.AppendLine("  • Single freshly-shuffled deck per hand.");
        sb.AppendLine("  • Dealer stands on all 17 (soft and hard).");
        sb.AppendLine("  • Blackjack pays 3:2. All other wins pay 1:1.");
        sb.AppendLine("  • Double on any first action (not after a split ace).");
        sb.AppendLine("  • Split equal-rank pairs once. Split aces get one card each.");
        sb.AppendLine("  • No insurance, no surrender.");
        sb.AppendLine();
        sb.AppendLine("Controls: [H] hit  [S] stand  [D] double  [P] split.");
        MessageBox.Query(_app, "Rules", sb.ToString(), "_OK");
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

    protected override void Dispose(bool disposing)
    {
        if (disposing && !_disposed)
        {
            _disposed = true;
            CancelFlashTimer();
            // If the user disconnected (or the screen tore down) mid-hand, settle to the
            // ledger so the wallet reflects what was on the table. Fire-and-forget since
            // we can't await in Dispose; the ledger runs against its own AppDbContext.
            if (_state is not null && !_state.HandComplete && !_committed)
            {
                try
                {
                    foreach (var hand in _state.PlayerHands)
                        if (!hand.Resolved) hand.Resolved = true;
                    BlackjackEngine.PlayDealer(_state);
                    BlackjackEngine.Settle(_state);
                    _state.HandComplete = true;
                    // Use a fresh CTS — ShutdownCts is being cancelled by base.Dispose.
                    var ledger = _services.GetService<IGameLedger>();
                    if (ledger is not null)
                    {
                        _committed = true;
                        var totalBet = _state.TotalBet;
                        var totalPayout = _state.TotalPayout;
                        var details = SerializeRoundDetails(_state);
                        _ = Task.Run(async () =>
                        {
                            try { await ledger.PlayRoundAsync(_user.Id, _game.Key, totalBet, totalPayout, details, CancellationToken.None); }
                            catch { /* swallow — best-effort settle on teardown */ }
                        });
                    }
                }
                catch { /* swallow */ }
            }
        }
        base.Dispose(disposing);
    }
}
