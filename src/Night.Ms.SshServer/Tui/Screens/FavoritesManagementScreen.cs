using System.Collections.ObjectModel;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Returned by FavoritesManagementScreen when the user picks Enter on a row. Null Result
// means the user backed out (Esc / D / R / J / K all keep them in the manager). When
// non-null, WeatherScreen activates the selected favorite as the new active location.
public sealed record FavoritesManagementResult(UserSavedLocation Selected);

// Saved-locations manager. Lists the user's favorites, lets them activate, delete, rename,
// or reorder. The F1..F9 mapping on WeatherScreen is by position in this list, so reorder
// is also "reassign the F-key bindings".
//
// Reorder swaps the SortOrder of two adjacent rows; gaps that develop from deletes are
// harmless because OrderBy(SortOrder).ThenBy(Id) keeps the order deterministic.
public sealed class FavoritesManagementScreen : BbsWindow
{
    private const int MaxFavoritesToShow = 9;

    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;
    private readonly ListView _list;
    private readonly BbsStatusLine _status;

    private List<UserSavedLocation> _favorites = new();
    private readonly TwoStepDelete<UserSavedLocation> _delete;

    public FavoritesManagementScreen(IApplication app, IServiceProvider services, User user)
        : base(app, services, user)
    {
        _app = app;
        _services = services;
        _user = user;
        Title = "ssh.night.ms — manage favorites — [Enter] use   [D] delete   [R] rename   [K/J] move up/down   [Esc] back";

        var header = new Label
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Text = "Favorites — top of the list is F1, bottom is F9.",
        };
        header.SetScheme(BbsTheme.Hint);

        _list = new ListView
        {
            X = 0,
            Y = 2,
            Width = Dim.Fill(),
            Height = Dim.Fill(3),
        };
        _list.KeyDown += OnListKeyDown;

        _status = new BbsStatusLine
        {
            X = 0,
            Y = Pos.AnchorEnd(2),
            Width = Dim.Fill(),
        };

        _delete = new TwoStepDelete<UserSavedLocation>(
            _status,
            id: f => f.Id,
            label: f => f.Label,
            commit: f => DeleteAsync(f).FireAndLog(_services, nameof(DeleteAsync)));

        Add(header, _list, _status);
        _list.SetFocus();

        InstallEscapeHandler(() => Result = null);

        LoadAsync().FireAndLog(_services, nameof(LoadAsync));
    }

    private void OnListKeyDown(object? sender, Key key)
    {
        if (_delete.TryHandle(key, SelectedFavorite()))
        {
            key.Handled = true;
            return;
        }
        _delete.Reset();

        if (key == Key.Enter)
        {
            key.Handled = true;
            var fav = SelectedFavorite();
            if (fav is null) return;
            Result = new FavoritesManagementResult(fav);
            _app.RequestStop();
        }
        else if (key.Matches(Key.R))
        {
            key.Handled = true;
            HandleRenameKey();
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
    }

    private UserSavedLocation? SelectedFavorite()
    {
        var idx = _list.SelectedItem ?? -1;
        if (idx < 0 || idx >= _favorites.Count) return null;
        return _favorites[idx];
    }

    private void HandleRenameKey()
    {
        var fav = SelectedFavorite();
        if (fav is null) return;

        // Reuse the same prompt the save flow uses — it handles the truncate-to-64 and
        // empty-trim rules for us.
        var newLabel = _app.Run(new SaveFavoritePromptScreen(_app, _services, _user, fav.Label)) as string;
        if (string.IsNullOrWhiteSpace(newLabel)) return;
        var trimmed = newLabel.Trim();
        if (trimmed == fav.Label) return;

        RenameAsync(fav, trimmed).FireAndLog(_services, nameof(RenameAsync));
    }

    private async Task LoadAsync(int? selectIndex = null)
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var favorites = await db.UserSavedLocations
                .Where(s => s.UserId == _user.Id)
                .OrderBy(s => s.SortOrder)
                .ThenBy(s => s.Id)
                .Take(MaxFavoritesToShow)
                .ToListAsync(Shutdown);

            _app.Invoke(() =>
            {
                _favorites = favorites;
                _list.SetSource<string>(new ObservableCollection<string>(_favorites.Select(FormatFavorite)));
                if (_favorites.Count == 0)
                {
                    _status.Set("No favorites yet. Press Esc, then 'S' on the weather screen to save one.");
                    return;
                }
                var clamped = selectIndex is { } i
                    ? Math.Clamp(i, 0, _favorites.Count - 1)
                    : Math.Clamp(_list.SelectedItem ?? 0, 0, _favorites.Count - 1);
                _list.SelectedItem = clamped;
                _status.Set(_favorites.Count == 1 ? "1 favorite." : $"{_favorites.Count} favorites.");
            });
        }
        catch (OperationCanceledException) { /* shutting down */ }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] couldn't load favorites: {ex.Message}"));
        }
    }

    private async Task DeleteAsync(UserSavedLocation fav)
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var row = await db.UserSavedLocations.FindAsync(new object?[] { fav.Id }, Shutdown);
            if (row is null) return;
            db.UserSavedLocations.Remove(row);
            await db.SaveChangesAsync(Shutdown);

            var nextIdx = Math.Max(0, (_list.SelectedItem ?? 0));
            _app.Invoke(() => _status.SetSuccess($"Deleted '{fav.Label}'."));
            await LoadAsync(nextIdx);
        }
        catch (OperationCanceledException) { /* shutting down */ }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] delete failed: {ex.Message}"));
        }
    }

    private async Task RenameAsync(UserSavedLocation fav, string newLabel)
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var row = await db.UserSavedLocations.FindAsync(new object?[] { fav.Id }, Shutdown);
            if (row is null) return;
            row.Label = newLabel;
            await db.SaveChangesAsync(Shutdown);

            _app.Invoke(() => _status.SetSuccess($"Renamed to '{newLabel}'."));
            await LoadAsync();
        }
        catch (OperationCanceledException) { /* shutting down */ }
        catch (DbUpdateException)
        {
            // Unique index on (user_id, label) — another favorite already uses this label.
            _app.Invoke(() => _status.SetWarning($"[!] another favorite already uses '{newLabel}'."));
        }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] rename failed: {ex.Message}"));
        }
    }

    private async Task MoveAsync(int delta)
    {
        var idx = _list.SelectedItem ?? -1;
        var newIdx = idx + delta;
        if (idx < 0 || newIdx < 0 || newIdx >= _favorites.Count) return;

        var current = _favorites[idx];
        var neighbor = _favorites[newIdx];

        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var rowA = await db.UserSavedLocations.FindAsync(new object?[] { current.Id }, Shutdown);
            var rowB = await db.UserSavedLocations.FindAsync(new object?[] { neighbor.Id }, Shutdown);
            if (rowA is null || rowB is null) return;
            (rowA.SortOrder, rowB.SortOrder) = (rowB.SortOrder, rowA.SortOrder);
            await db.SaveChangesAsync(Shutdown);
            await LoadAsync(newIdx);
        }
        catch (OperationCanceledException) { /* shutting down */ }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] move failed: {ex.Message}"));
        }
    }

    private static string FormatFavorite(UserSavedLocation f) =>
        // The F-key prefix is implicit (position in list); show it explicitly so the user
        // doesn't have to count rows. Coords are rounded to 2 decimals for compactness.
        $"  {f.Label,-24}  {f.Canonical ?? string.Empty,-40}  ({f.Latitude:F2}, {f.Longitude:F2})";

}
