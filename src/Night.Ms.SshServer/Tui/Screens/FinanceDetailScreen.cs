using System.Collections.ObjectModel;
using System.Globalization;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Providers.Finance;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Drill-in detail for a single watchlist row. Header: symbol/name/price/change. Body: a
// multi-row block chart from the same series the sparkline used, scaled up. Stats row: open,
// day hi/lo, 52-week hi/lo, volume. News pane below: filtered to this symbol when it's a
// stock; for crypto/FX we fall back to the broad market headlines (the Yahoo RSS endpoint
// doesn't know coin ids).
public sealed class FinanceDetailScreen : BbsWindow
{
    private const int ChartHeight = 10;
    private const int MaxNewsItems = 10;

    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;
    private readonly WatchlistKind _kind;
    private readonly string _canonical;
    private readonly string _symbol;

    private readonly Label _hintBar;
    private readonly Label _header;
    private readonly Label[] _chartRows = new Label[ChartHeight];
    private readonly Label _stats;
    private readonly Label _newsHeader;
    private readonly ListView _news;
    private readonly BbsStatusLine _status;

    private List<NewsHeadline> _newsModel = [];

    public FinanceDetailScreen(IApplication app, IServiceProvider services, User user, WatchlistKind kind, string canonical, string symbol)
        : base(app, services, user)
    {
        _app = app;
        _services = services;
        _user = user;
        _kind = kind;
        _canonical = canonical;
        _symbol = symbol;
        Title = $"ssh.night.ms — {symbol} — [R] refresh  [Esc] back";

        _hintBar = new Label
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Text = "[R] refresh   [Enter] open news item   [Esc] back to watchlist",
        };
        _hintBar.SetScheme(BbsTheme.Hint);

        _header = new Label
        {
            X = 0,
            Y = 1,
            Width = Dim.Fill(),
            Text = $"{symbol} — loading…",
        };
        _header.SetScheme(BbsTheme.Header_);

        // Chart rendered as a stack of Label rows so each line gets the same scheme without
        // a custom View. Y-coordinates left fixed so layout matches the verification mock.
        for (var r = 0; r < ChartHeight; r++)
        {
            _chartRows[r] = new Label
            {
                X = 0,
                Y = 3 + r,
                Width = Dim.Fill(),
                Text = string.Empty,
            };
            _chartRows[r].SetScheme(BbsTheme.Status);
            Add(_chartRows[r]);
        }

        _stats = new Label
        {
            X = 0,
            Y = 3 + ChartHeight + 1,
            Width = Dim.Fill(),
            Text = string.Empty,
        };
        _stats.SetScheme(BbsTheme.Hint);

        _newsHeader = new Label
        {
            X = 0,
            Y = 3 + ChartHeight + 3,
            Width = Dim.Fill(),
            Text = "── related news ─────────────────────────────────────────────",
        };
        _newsHeader.SetScheme(BbsTheme.Header_);

        _news = new ListView
        {
            X = 0,
            Y = 3 + ChartHeight + 4,
            Width = Dim.Fill(),
            Height = Dim.Fill(3),
        };
        _news.KeyDown += OnNewsKeyDown;

        _status = new BbsStatusLine
        {
            X = 0,
            Y = Pos.AnchorEnd(2),
            Width = Dim.Fill(),
        };

        Add(_hintBar, _header, _stats, _newsHeader, _news, _status);
        _news.SetFocus();

        InstallEscapeHandler();
        KeyDown += (_, key) =>
        {
            if (key.Matches(Key.R))
            {
                key.Handled = true;
                ReloadAsync().FireAndLog(_services, nameof(ReloadAsync));
            }
        };

        ReloadAsync().FireAndLog(_services, nameof(ReloadAsync));
    }

    private void OnNewsKeyDown(object? sender, Key key)
    {
        if (key == Key.Enter)
        {
            var idx = _news.SelectedItem ?? -1;
            if (idx >= 0 && idx < _newsModel.Count)
            {
                var item = _newsModel[idx];
                if (Uri.TryCreate(item.Url, UriKind.Absolute, out var uri)
                    && (uri.Scheme == Uri.UriSchemeHttp || uri.Scheme == Uri.UriSchemeHttps))
                {
                    key.Handled = true;
                    _app.Run(new ReaderScreen(_app, _services, _user, uri));
                }
            }
        }
    }

    private async Task ReloadAsync()
    {
        _app.Invoke(() => _status.Set("loading detail…"));
        var finance = _services.GetRequiredService<IFinanceProvider>();
        var newsProvider = _services.GetRequiredService<IFinanceNewsProvider>();

        var detailTask = finance.GetDetailAsync(_kind, _canonical, Shutdown);
        // For stocks the canonical IS the ticker the RSS endpoint accepts. For crypto/FX we
        // pass an empty list and the provider falls back to broad market headlines.
        var tickers = _kind == WatchlistKind.Stock ? new[] { _canonical } : Array.Empty<string>();
        var newsTask = newsProvider.GetForTickersAsync(tickers, MaxNewsItems, Shutdown);

        await Task.WhenAll(detailTask, newsTask).ConfigureAwait(false);
        var detail = detailTask.Result;
        var news = newsTask.Result;

        _app.Invoke(() =>
        {
            if (detail is null)
            {
                _header.Text = $"{_symbol} — data unavailable";
                _stats.Text = string.Empty;
                foreach (var r in _chartRows) r.Text = string.Empty;
            }
            else
            {
                _header.Text = FormatHeader(detail);
                _stats.Text = FormatStats(detail, _kind);
                RenderChart(detail.Series);
            }
            _newsModel = news.ToList();
            _news.SetSource<string>(new ObservableCollection<string>(_newsModel.Select(FormatHeadline)));
            _status.Set(detail is null ? "[!] couldn't load detail." : $"updated {_user.FormatClockWithSeconds(DateTimeOffset.Now)}");
        });
    }

    private void RenderChart(IReadOnlyList<double> series)
    {
        var width = Math.Max(20, (int)(Frame.Width) - 2);
        var lines = BigChart.Render(series, width, ChartHeight);
        for (var r = 0; r < ChartHeight; r++)
            _chartRows[r].Text = r < lines.Count ? lines[r] : string.Empty;
    }

    private static string FormatHeader(FinanceDetail d)
    {
        var q = d.Quote;
        var price = FormatPrice(q.Price, q.Currency);
        var changeSign = q.Change >= 0 ? "+" : "";
        var pctSign = q.ChangePct >= 0 ? "+" : "";
        var ts = q.AsOf.ToLocalTime().ToString("HH:mm", CultureInfo.InvariantCulture);
        return $"{q.DisplayName}   {price}   {changeSign}{q.Change:N2}   ({pctSign}{q.ChangePct:N2}%)   as of {ts}";
    }

    private static string FormatStats(FinanceDetail d, WatchlistKind kind)
    {
        // Stats vocabulary varies by data source:
        //   Stocks (Yahoo)    — intraday + 52-week + volume.
        //   Crypto (CoinGecko) — intraday min/max (derived from market_chart) + open; no 52w
        //                        because we don't make the extra /coins/{id} request that
        //                        would supply it. Volume is null in the same vein.
        //   FX (Frankfurter)  — daily EOD only, so "Day" doesn't exist as a concept. We use
        //                        the 365-day range stored in Week52* and label it "1y range".
        string fmt(decimal? v) => v is { } x ? x.ToString("N2", CultureInfo.InvariantCulture) : "—";
        string fmtLong(long? v) => v is { } x ? x.ToString("N0", CultureInfo.InvariantCulture) : "—";
        return kind switch
        {
            WatchlistKind.Stock =>
                $"Open {fmt(d.Open)}   Day {fmt(d.DayLow)}–{fmt(d.DayHigh)}   52w {fmt(d.Week52Low)}–{fmt(d.Week52High)}   Vol {fmtLong(d.Volume)}",
            WatchlistKind.Crypto =>
                $"Open {fmt(d.Open)}   Day {fmt(d.DayLow)}–{fmt(d.DayHigh)}",
            WatchlistKind.Fx =>
                $"1y open {fmt(d.Open)}   1y range {fmt(d.Week52Low)}–{fmt(d.Week52High)}",
            _ => string.Empty,
        };
    }

    private static string FormatPrice(decimal price, string currency)
    {
        var decimals = price >= 1m ? 2 : 4;
        return $"{FormatHelpers.CurrencyGlyph(currency)}{price.ToString("N" + decimals, CultureInfo.InvariantCulture)} {currency}";
    }

    private static string FormatHeadline(NewsHeadline h)
    {
        var age = FormatHelpers.HumanizeAge(DateTimeOffset.UtcNow - h.PublishedAt);
        return $"  {h.Title}  ({age})";
    }

}
