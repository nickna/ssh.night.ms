using System.Collections.ObjectModel;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public sealed class ForumListScreen : Window
{
    private readonly IApplication _app;
    private readonly List<Forum> _forums;

    public ForumListScreen(IApplication app, AppDbContext db)
    {
        _app = app;
        Title = "ssh.night.ms — boards — [Esc] back to lobby";
        BbsTheme.ApplyWindow(this);

        _forums = db.Forums.OrderBy(f => f.SortOrder).ThenBy(f => f.Name).ToList();

        var listView = new ListView
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Height = Dim.Fill(),
        };
        listView.SetSource<string>(new ObservableCollection<string>(_forums.Select(f => $"{f.Name,-20} {f.Description}")));

        listView.KeyDown += (_, key) =>
        {
            if (key == Key.Enter)
            {
                var idx = listView.SelectedItem ?? 0;
                if (idx >= 0 && idx < _forums.Count)
                {
                    Result = _forums[idx];
                    _app.RequestStop();
                    key.Handled = true;
                }
            }
        };

        Add(listView);
        listView.SetFocus();

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                Result = null;
                _app.RequestStop();
                key.Handled = true;
            }
        };
    }
}
