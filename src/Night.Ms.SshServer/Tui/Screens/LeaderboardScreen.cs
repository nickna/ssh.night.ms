using System.Text;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Doors.Leaderboards;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Three views over game_rounds: biggest single wins, lifetime cumulative net, and the last
// 7 days as a "hot streak" board. Switched with the number keys 1/2/3. All three load
// async on first selection and re-load on tab switch so a player who just hit a Royal can
// pop in and see themselves at the top.
internal sealed class LeaderboardScreen : BbsWindow
{
    private const int TopN = 10;
    private const int HotStreakWindowDays = 7;

    private enum View { TopWins, LifetimeNet, HotStreaks }

    private readonly IApplication _app;
    private readonly IServiceProvider _services;

    private readonly Label _tabsLabel;
    private readonly Label _headerLabel;
    private readonly Label _bodyLabel;

    private View _view = View.TopWins;
    private CancellationTokenSource? _loadCts;

    public LeaderboardScreen(IApplication app, IServiceProvider services, User user)
        : base(app, services, user)
    {
        _app = app;
        _services = services;

        Title = $"ssh.night.ms — leaderboards — {user.Handle}";

        var header = new Label
        {
            X = 2, Y = 0, Width = Dim.Fill(2),
            Text = "Leaderboards",
        };
        header.SetScheme(BbsTheme.Header_);

        _tabsLabel = new Label
        {
            X = 2, Y = 2, Width = Dim.Fill(2),
            Text = string.Empty,
        };
        _tabsLabel.SetScheme(BbsTheme.Hint);

        _headerLabel = new Label
        {
            X = 2, Y = 4, Width = Dim.Fill(2),
            Text = string.Empty,
        };

        _bodyLabel = new Label
        {
            X = 2, Y = 6, Width = Dim.Fill(2),
            Height = Dim.Fill(3),
            Text = "Loading…",
        };

        var hint = new Label
        {
            X = 2, Y = Pos.AnchorEnd(2), Width = Dim.Fill(2),
            Text = "[1] Top Wins    [2] Lifetime Net    [3] Last 7 Days    [Esc] back",
        };
        hint.SetScheme(BbsTheme.Hint);

        Add(header, _tabsLabel, _headerLabel, _bodyLabel, hint);

        KeyDown += OnKey;
        InstallEscapeHandler();

        RenderTabs();
        LoadCurrentAsync().FireAndLog(services, nameof(LoadCurrentAsync));
    }

    private void OnKey(object? _, Key key)
    {
        if (key == Key.D1) { key.Handled = true; SwitchTo(View.TopWins); return; }
        if (key == Key.D2) { key.Handled = true; SwitchTo(View.LifetimeNet); return; }
        if (key == Key.D3) { key.Handled = true; SwitchTo(View.HotStreaks); return; }
    }

    private void SwitchTo(View view)
    {
        if (_view == view) return;
        _view = view;
        RenderTabs();
        _bodyLabel.Text = "Loading…";
        _headerLabel.Text = string.Empty;
        LoadCurrentAsync().FireAndLog(_services, nameof(LoadCurrentAsync));
    }

    private void RenderTabs()
    {
        string Mark(View v, string label) => _view == v ? $"[ {label} ]" : $"  {label}  ";
        _tabsLabel.Text = string.Join("    ", new[]
        {
            Mark(View.TopWins, "Top Wins"),
            Mark(View.LifetimeNet, "Lifetime Net"),
            Mark(View.HotStreaks, $"Last {HotStreakWindowDays} Days"),
        });
    }

    private async Task LoadCurrentAsync()
    {
        _loadCts?.Cancel();
        _loadCts = CancellationTokenSource.CreateLinkedTokenSource(Shutdown);
        var ct = _loadCts.Token;
        var view = _view;

        // Each load gets its own DI scope so the AppDbContext on the leaderboard service
        // doesn't outlive the request — and so two consecutive tab switches don't share a
        // tracked-entity bag.
        await using var scope = _services.CreateAsyncScope();
        var svc = scope.ServiceProvider.GetRequiredService<ILeaderboardService>();

        IReadOnlyList<LeaderboardEntry> entries;
        string sectionHeader;
        try
        {
            entries = view switch
            {
                View.TopWins => await svc.GetTopSingleWinsAsync(TopN, ct),
                View.LifetimeNet => await svc.GetTopLifetimeNetAsync(TopN, ct),
                View.HotStreaks => await svc.GetHotStreaksAsync(TopN, HotStreakWindowDays, ct),
                _ => Array.Empty<LeaderboardEntry>(),
            };
            sectionHeader = view switch
            {
                View.TopWins => "Biggest single wins ever",
                View.LifetimeNet => "Cumulative net coins, all games combined",
                View.HotStreaks => $"Cumulative net coins over the last {HotStreakWindowDays} days",
                _ => string.Empty,
            };
        }
        catch (OperationCanceledException) { return; }

        if (ct.IsCancellationRequested) return;

        _app.Invoke(() =>
        {
            _headerLabel.Text = sectionHeader;
            _bodyLabel.Text = entries.Count == 0
                ? "(no rounds played yet)"
                : Format(view, entries);
        });
    }

    private static string Format(View view, IReadOnlyList<LeaderboardEntry> entries)
    {
        var sb = new StringBuilder();
        if (view == View.TopWins)
        {
            sb.AppendLine("  #   Handle              Game          Net");
            sb.AppendLine("  ─── ─────────────────── ──────────── ──────");
            foreach (var e in entries)
            {
                sb.AppendLine($"  {e.Rank,2}.  {Truncate(e.Handle, 18),-18}  {e.GameKey,-10}  +{e.Value,5}");
            }
        }
        else
        {
            sb.AppendLine("  #   Handle              Net");
            sb.AppendLine("  ─── ─────────────────── ───────");
            foreach (var e in entries)
            {
                sb.AppendLine($"  {e.Rank,2}.  {Truncate(e.Handle, 18),-18}  {e.Value,+7}");
            }
        }
        return sb.ToString();
    }

    private static string Truncate(string s, int max) =>
        s.Length <= max ? s : s.AsSpan(0, max - 1).ToString() + "…";

    protected override void Dispose(bool disposing)
    {
        if (disposing)
        {
            try { _loadCts?.Cancel(); } catch { }
            _loadCts?.Dispose();
        }
        base.Dispose(disposing);
    }
}
