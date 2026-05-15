using System.Collections.ObjectModel;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public sealed class ForumListScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly List<Forum> _forums;

    public ForumListScreen(IApplication app, IServiceProvider services, AppDbContext db, User user)
        : base(app, services, user)
    {
        _app = app;
        Title = "ssh.night.ms — boards — [Esc] back to lobby";

        _forums = db.Forums.OrderBy(f => f.SortOrder).ThenBy(f => f.Name).ToList();

        var listView = new ListView
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Height = Dim.Fill(1),
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

        InstallEscapeHandler(() => Result = null);
    }
}
