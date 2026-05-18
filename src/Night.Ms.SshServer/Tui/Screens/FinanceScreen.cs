using System.Collections.ObjectModel;
using System.Globalization;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Providers.Finance;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Finance dashboard: unified watchlist (stocks + crypto + FX) on top, finance news pane on
// the bottom. Mirrors NewsScreen's load-on-open + manual-[R]-refresh pattern. Defaults seed
// in on first open so a new user sees a populated screen rather than an empty prompt.
//
// Per row: SYMBOL  TYPE  PRICE  CHG  %  SPARK. Columns are fixed-width strings so the
// ListView (which only displays text) stays aligned. Sparkline is rendered with block glyphs
// inside a small bounded width.
public sealed class FinanceScreen : BbsWindow
{
    private const int MaxRowsInWatchlist = 12;
    private const int SparklineWidth = 12;
    private const int MaxNewsItems = 15;
    // Hard cap on watchlist size per user. Twelve rows fit on-screen without scrolling and
    // anything beyond ~20 starts to strain the per-refresh fan-out against CoinGecko's free
    // tier (5–15 req/min). Mirrors the 9-favorite cap on WeatherScreen.
    private const int MaxWatchlistRows = 20;

    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;

    private readonly Label _hintBar;
    private readonly Label _columnHeader;
    private readonly ListView _rows;
    private readonly Label _newsHeader;
    private readonly ListView _news;
    private readonly BbsStatusLine _status;

    private List<WatchlistRow> _rowsModel = [];
    private List<NewsHeadline> _newsModel = [];
    private readonly TwoStepDelete<WatchlistRow> _delete;

    public FinanceScreen(IApplication app, IServiceProvider services, User user)
        : base(app, services, user)
    {
        _app = app;
        _services = services;
        _user = user;
        Title = $"ssh.night.ms — finance — {user.Handle}";

        _hintBar = new Label
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Text = "[A]dd  [E]dit  [D]el  [R]efresh  [N] news  [K/J] move  [Enter] detail  [Esc] back",
        };
        _hintBar.SetScheme(BbsTheme.Hint);

        _columnHeader = new Label
        {
            X = 0,
            Y = 2,
            Width = Dim.Fill(),
            Text = FormatHeader(),
        };
        _columnHeader.SetScheme(BbsTheme.Header_);

        // Bounded watchlist height so the news pane always gets at least 4 rows on an 80×24
        // PTY (header + hint + watchlist + news header + news + status + footer ≈ 24 lines).
        _rows = new ListView
        {
            X = 0,
            Y = 3,
            Width = Dim.Fill(),
            Height = MaxRowsInWatchlist,
        };
        _rows.KeyDown += OnRowsKeyDown;

        _newsHeader = new Label
        {
            X = 0,
            Y = 3 + MaxRowsInWatchlist,
            Width = Dim.Fill(),
            Text = "── finance news ─────────────────────────────────────────────",
        };
        _newsHeader.SetScheme(BbsTheme.Header_);

        _news = new ListView
        {
            X = 0,
            Y = 4 + MaxRowsInWatchlist,
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

        _delete = new TwoStepDelete<WatchlistRow>(
            _status,
            id: r => r.Item.Id,
            label: r => r.Item.Symbol,
            commit: r => DeleteAsync(r.Item).FireAndLog(_services, nameof(DeleteAsync)));

        Add(_hintBar, _columnHeader, _rows, _newsHeader, _news, _status);
        _rows.SetFocus();

        InstallEscapeHandler();
        // Global KeyDown for hotkeys that work from either focus (refresh, news toggle).
        KeyDown += OnScreenKeyDown;

        ReloadAsync().FireAndLog(_services, nameof(ReloadAsync));
    }

    private void OnScreenKeyDown(object? sender, Key key)
    {
        // Refresh and switch-to-news work from anywhere; rows/news local handlers cover
        // the rest. Esc is handled by InstallEscapeHandler.
        if (key.Matches(Key.R))
        {
            key.Handled = true;
            _delete.Reset();
            ReloadAsync().FireAndLog(_services, nameof(ReloadAsync));
        }
        else if (key.Matches(Key.N))
        {
            key.Handled = true;
            _news.SetFocus();
        }
    }

    private void OnRowsKeyDown(object? sender, Key key)
    {
        if (_delete.TryHandle(key, SelectedRow()))
        {
            key.Handled = true;
            return;
        }
        _delete.Reset();

        if (key.Matches(Key.A))
        {
            key.Handled = true;
            OpenAddPrompt(prefill: null);
        }
        else if (key.Matches(Key.E))
        {
            key.Handled = true;
            var sel = SelectedRow();
            if (sel is not null) OpenEditPrompt(sel);
        }
        else if (key.Matches(Key.K))
        {
            key.Handled = true;
            MoveAsync(-1).FireAndLog(_services, nameof(MoveAsync));
        }
        else if (key.Matches(Key.J))
        {
            key.Handled = true;
            MoveAsync(+1).FireAndLog(_services, nameof(MoveAsync));
        }
        else if (key == Key.Enter)
        {
            var sel = SelectedRow();
            if (sel is not null)
            {
                key.Handled = true;
                _app.Run(new FinanceDetailScreen(_app, _services, _user, sel.Item.Kind, sel.Item.Canonical, sel.Item.Symbol));
            }
        }
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
        else if (key == Key.CursorUp && (_news.SelectedItem ?? 0) == 0)
        {
            // At the top of news, pressing up bounces focus back to the watchlist.
            key.Handled = true;
            _rows.SetFocus();
        }
    }

    private WatchlistRow? SelectedRow()
    {
        var idx = _rows.SelectedItem ?? -1;
        if (idx < 0 || idx >= _rowsModel.Count) return null;
        return _rowsModel[idx];
    }

    private void OpenAddPrompt(string? prefill)
    {
        var result = _app.Run(new AddWatchlistItemPromptScreen(_app, _services, _user, prefill)) as AddWatchlistItemResult;
        if (result is null) return;
        SaveNewItemAsync(result).FireAndLog(_services, nameof(SaveNewItemAsync));
    }

    private void OpenEditPrompt(WatchlistRow row)
    {
        var result = _app.Run(new AddWatchlistItemPromptScreen(_app, _services, _user, row.Item.Symbol)) as AddWatchlistItemResult;
        if (result is null) return;
        UpdateItemAsync(row.Item.Id, result).FireAndLog(_services, nameof(UpdateItemAsync));
    }

    private async Task SaveNewItemAsync(AddWatchlistItemResult res)
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var existing = await db.UserWatchlistItems
                .Where(w => w.UserId == _user.Id)
                .CountAsync(Shutdown);
            if (existing >= MaxWatchlistRows)
            {
                _app.Invoke(() => _status.SetWarning($"[!] watchlist is full ({MaxWatchlistRows}). Delete a symbol first."));
                return;
            }
            var nextSort = await db.UserWatchlistItems
                .Where(w => w.UserId == _user.Id)
                .Select(w => (int?)w.SortOrder)
                .MaxAsync(Shutdown) ?? -1;
            db.UserWatchlistItems.Add(new UserWatchlistItem
            {
                UserId = _user.Id,
                Symbol = res.Symbol,
                Canonical = res.Canonical,
                Kind = res.Kind,
                SortOrder = nextSort + 1,
                CreatedAt = DateTimeOffset.UtcNow,
            });
            await db.SaveChangesAsync(Shutdown);
            _app.Invoke(() => _status.SetSuccess($"Added {res.Symbol}."));
            await ReloadAsync();
        }
        catch (DbUpdateException)
        {
            _app.Invoke(() => _status.SetWarning($"[!] '{res.Symbol}' (→ {res.Canonical}) is already on your watchlist."));
        }
        catch (OperationCanceledException) { /* shutting down */ }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] add failed: {ex.Message}"));
        }
    }

    private async Task UpdateItemAsync(long id, AddWatchlistItemResult res)
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var row = await db.UserWatchlistItems.FindAsync([id], Shutdown);
            if (row is null) return;
            row.Symbol = res.Symbol;
            row.Canonical = res.Canonical;
            row.Kind = res.Kind;
            await db.SaveChangesAsync(Shutdown);
            _app.Invoke(() => _status.SetSuccess($"Updated {res.Symbol}."));
            await ReloadAsync();
        }
        catch (DbUpdateException)
        {
            _app.Invoke(() => _status.SetWarning($"[!] another row already uses '{res.Canonical}'."));
        }
        catch (OperationCanceledException) { /* shutting down */ }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] update failed: {ex.Message}"));
        }
    }

    private async Task DeleteAsync(UserWatchlistItem item)
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var row = await db.UserWatchlistItems.FindAsync([item.Id], Shutdown);
            if (row is null) return;
            db.UserWatchlistItems.Remove(row);
            await db.SaveChangesAsync(Shutdown);
            _app.Invoke(() => _status.SetSuccess($"Deleted {item.Symbol}."));
            await ReloadAsync();
        }
        catch (OperationCanceledException) { /* shutting down */ }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] delete failed: {ex.Message}"));
        }
    }

    private async Task MoveAsync(int delta)
    {
        var idx = _rows.SelectedItem ?? -1;
        var newIdx = idx + delta;
        if (idx < 0 || newIdx < 0 || newIdx >= _rowsModel.Count) return;
        var a = _rowsModel[idx].Item;
        var b = _rowsModel[newIdx].Item;
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var rowA = await db.UserWatchlistItems.FindAsync([a.Id], Shutdown);
            var rowB = await db.UserWatchlistItems.FindAsync([b.Id], Shutdown);
            if (rowA is null || rowB is null) return;
            (rowA.SortOrder, rowB.SortOrder) = (rowB.SortOrder, rowA.SortOrder);
            await db.SaveChangesAsync(Shutdown);
            await ReloadAsync(selectIndex: newIdx);
        }
        catch (OperationCanceledException) { /* shutting down */ }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] move failed: {ex.Message}"));
        }
    }

    private async Task ReloadAsync(int? selectIndex = null)
    {
        _app.Invoke(() => _status.Set("loading…"));

        List<UserWatchlistItem> items;
        try
        {
            items = await LoadOrSeedWatchlistAsync().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] couldn't load watchlist: {ex.Message}"));
            return;
        }

        var finance = _services.GetRequiredService<IFinanceProvider>();
        var newsProvider = _services.GetRequiredService<IFinanceNewsProvider>();

        // Per-row quote + sparkline in parallel; news in parallel with both.
        var quoteTasks = items.Select(i => finance.GetQuoteAsync(i.Kind, i.Canonical, Shutdown)).ToArray();
        var sparkTasks = items.Select(i => finance.GetSparklineAsync(i.Kind, i.Canonical, Shutdown)).ToArray();
        var stockTickers = items.Where(i => i.Kind == WatchlistKind.Stock).Select(i => i.Canonical).ToList();
        var newsTask = newsProvider.GetForTickersAsync(stockTickers, MaxNewsItems, Shutdown);

        await Task.WhenAll([..quoteTasks, ..sparkTasks, newsTask]).ConfigureAwait(false);

        var model = new List<WatchlistRow>(items.Count);
        for (var i = 0; i < items.Count; i++)
            model.Add(new WatchlistRow(items[i], quoteTasks[i].Result, sparkTasks[i].Result));
        var news = newsTask.Result;

        _app.Invoke(() =>
        {
            _rowsModel = model;
            _newsModel = news.ToList();
            _rows.SetSource<string>(new ObservableCollection<string>(_rowsModel.Select(FormatRow)));
            _news.SetSource<string>(new ObservableCollection<string>(_newsModel.Select(FormatHeadline)));
            if (_rowsModel.Count > 0)
            {
                var clamped = selectIndex is { } s ? Math.Clamp(s, 0, _rowsModel.Count - 1) : Math.Clamp(_rows.SelectedItem ?? 0, 0, _rowsModel.Count - 1);
                _rows.SelectedItem = clamped;
            }

            var loaded = _rowsModel.Count(r => r.Quote is not null);
            var stamp = _user.FormatClockWithSeconds(DateTimeOffset.Now);
            if (_rowsModel.Count == 0)
                _status.Set("Watchlist is empty. Press A to add a symbol.");
            else
                _status.Set($"{loaded}/{_rowsModel.Count} quotes · {_newsModel.Count} news · updated {stamp}");
        });
    }

    // Pulls the user's rows; if empty, seeds the curated default list inside the same scope
    // and returns the freshly-inserted set. Subsequent opens skip the seed because the rows
    // already exist.
    private async Task<List<UserWatchlistItem>> LoadOrSeedWatchlistAsync()
    {
        await using var scope = _services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var rows = await db.UserWatchlistItems
            .Where(w => w.UserId == _user.Id)
            .OrderBy(w => w.SortOrder)
            .ThenBy(w => w.Id)
            .ToListAsync(Shutdown);
        if (rows.Count > 0) return rows;

        var defaults = new (string Symbol, WatchlistKind Kind, string Canonical)[]
        {
            ("AAPL", WatchlistKind.Stock, "AAPL"),
            ("MSFT", WatchlistKind.Stock, "MSFT"),
            ("BTC", WatchlistKind.Crypto, "bitcoin"),
            ("ETH", WatchlistKind.Crypto, "ethereum"),
            ("EUR/USD", WatchlistKind.Fx, "EURUSD"),
        };
        var now = DateTimeOffset.UtcNow;
        for (var i = 0; i < defaults.Length; i++)
        {
            var (sym, kind, canon) = defaults[i];
            db.UserWatchlistItems.Add(new UserWatchlistItem
            {
                UserId = _user.Id,
                Symbol = sym,
                Canonical = canon,
                Kind = kind,
                SortOrder = i,
                CreatedAt = now,
            });
        }
        try
        {
            await db.SaveChangesAsync(Shutdown);
        }
        catch (DbUpdateException)
        {
            // Another session seeded concurrently; just read what's there now.
        }
        return await db.UserWatchlistItems
            .Where(w => w.UserId == _user.Id)
            .OrderBy(w => w.SortOrder)
            .ThenBy(w => w.Id)
            .ToListAsync(Shutdown);
    }

    private static string FormatHeader() =>
        $"{"SYMBOL",-10} {"TYPE",-8} {"PRICE",12} {"CHG",10} {"%",8}   SPARK";

    private static string FormatRow(WatchlistRow row)
    {
        var sym = FormatHelpers.Truncate(row.Item.Symbol, 10);
        var type = row.Item.Kind switch
        {
            WatchlistKind.Stock => "stock",
            WatchlistKind.Crypto => "crypto",
            WatchlistKind.Fx => "fx",
            _ => "?",
        };
        if (row.Quote is null)
        {
            return $"{sym,-10} {type,-8} {"—",12} {"—",10} {"—",8}   {"—",-12}";
        }
        var q = row.Quote;
        var price = FormatPrice(q.Price, q.Currency);
        var chg = FormatChange(q.Change);
        var pct = FormatPct(q.ChangePct);
        var spark = Sparkline.Render(row.Sparkline, SparklineWidth);
        if (string.IsNullOrEmpty(spark)) spark = "—";
        return $"{sym,-10} {type,-8} {price,12} {chg,10} {pct,8}   {spark,-12}";
    }

    private static string FormatPrice(decimal price, string currency)
    {
        // Crypto and stock prices fit in 2dp; sub-dollar crypto (DOGE etc) needs more
        // precision — switch to 4dp when the price is small.
        var decimals = price >= 1m ? 2 : 4;
        return $"{FormatHelpers.CurrencyGlyph(currency)}{price.ToString("N" + decimals, CultureInfo.InvariantCulture)}";
    }

    private static string FormatChange(decimal change)
    {
        var sign = change >= 0 ? "+" : "";
        var decimals = Math.Abs(change) >= 1m ? 2 : 4;
        return $"{sign}{change.ToString("N" + decimals, CultureInfo.InvariantCulture)}";
    }

    private static string FormatPct(decimal pct)
    {
        var sign = pct >= 0 ? "+" : "";
        return $"{sign}{pct.ToString("N2", CultureInfo.InvariantCulture)}%";
    }

    private static string FormatHeadline(NewsHeadline h)
    {
        var age = FormatHelpers.HumanizeAge(DateTimeOffset.UtcNow - h.PublishedAt);
        return $"  {h.Title}  ({age})";
    }


    // In-screen model: the persisted row plus its most recent fetched quote + sparkline.
    // Quote is null when the upstream call failed; the row still renders with "—" placeholders.
    private sealed record WatchlistRow(
        UserWatchlistItem Item,
        FinanceQuote? Quote,
        IReadOnlyList<double>? Sparkline);
}
