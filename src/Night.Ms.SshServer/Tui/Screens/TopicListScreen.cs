using System.Collections.ObjectModel;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public enum TopicListResult { Back, OpenTopic, NewTopic }

public sealed class TopicListScreen : BbsWindow
{
    private readonly IApplication _app;
    public Topic? SelectedTopic { get; private set; }

    public TopicListScreen(IApplication app, IServiceProvider services, AppDbContext db, User user, Forum forum)
        : base(app, services, user)
    {
        _app = app;
        Title = $"boards/{forum.Name} — [N]ew topic — [Esc] back";

        var topics = db.Topics
            .Where(t => t.ForumId == forum.Id)
            .OrderByDescending(t => t.LastPostAt)
            .Take(50)
            .ToList();

        var lastReadByTopic = db.PostReads
            .Where(r => r.UserId == user.Id && topics.Select(t => t.Id).Contains(r.TopicId))
            .ToDictionary(r => r.TopicId, r => r.LastReadAt);

        var rows = topics.Select(t =>
        {
            lastReadByTopic.TryGetValue(t.Id, out var lastRead);
            var unread = t.LastPostAt > lastRead ? "*" : " ";
            return $"{unread} {user.FormatDateTime(t.LastPostAt)}  {t.Title}";
        }).ToList();

        if (rows.Count == 0)
        {
            rows.Add("(no topics yet — press N to start one)");
        }

        var listView = new ListView
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Height = Dim.Fill(1),
        };
        listView.SetSource<string>(new ObservableCollection<string>(rows));

        listView.KeyDown += (_, key) =>
        {
            if (key == Key.Enter && topics.Count > 0)
            {
                var idx = listView.SelectedItem ?? 0;
                if (idx >= 0 && idx < topics.Count)
                {
                    SelectedTopic = topics[idx];
                    Result = TopicListResult.OpenTopic;
                    _app.RequestStop();
                    key.Handled = true;
                }
            }
        };

        Add(listView);
        listView.SetFocus();

        InstallEscapeHandler(() => Result = TopicListResult.Back);
        KeyDown += (_, key) =>
        {
            if (key == Key.N || key == Key.N.WithShift)
            {
                Result = TopicListResult.NewTopic;
                _app.RequestStop();
                key.Handled = true;
            }
        };
    }
}
