using System.Text;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public sealed class ThreadScreen : BbsWindow
{
    private readonly IServiceProvider _services;
    private readonly IApplication _app;
    private readonly User _user;
    private readonly Topic _topic;
    private readonly TextView _log;
    private readonly TextField _input;
    private readonly Label _status;

    public ThreadScreen(IServiceProvider services, IApplication app, User user, Topic topic)
        : base(app, services, user)
    {
        _services = services;
        _app = app;
        _user = user;
        _topic = topic;
        Title = $"thread — {topic.Title} — [Esc] back";

        _log = new TextView
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Height = Dim.Fill(3),
            ReadOnly = true,
            WordWrap = true,
        };

        _status = new Label
        {
            X = 0,
            Y = Pos.Bottom(_log),
            Width = Dim.Fill(),
            Text = "Type a reply and press Enter to post.",
        };
        _status.SetScheme(BbsTheme.Status);

        _input = new TextField
        {
            X = 0,
            Y = Pos.Bottom(_status),
            Width = Dim.Fill(),
        };
        _input.SetScheme(BbsTheme.Input);

        _input.KeyDown += (_, key) =>
        {
            if (key == Key.Enter)
            {
                key.Handled = true;
                var text = (_input.Text ?? string.Empty).Trim();
                if (text.Length > 0)
                {
                    _input.Text = string.Empty;
                    PostReplyAsync(text).FireAndLog(_services, nameof(PostReplyAsync));
                }
            }
        };

        Add(_log, _status, _input);
        _input.SetFocus();

        InstallEscapeHandler();

        LoadAsync().FireAndLog(_services, nameof(LoadAsync));
    }

    private async Task LoadAsync()
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var posts = await db.Posts
                .Where(p => p.TopicId == _topic.Id)
                .OrderBy(p => p.CreatedAt)
                .Take(200)
                .Include(p => p.CreatedBy)
                .ToListAsync();

            var sb = new StringBuilder();
            foreach (var p in posts)
            {
                sb.Append($"[{_user.FormatDateTime(p.CreatedAt)}] {p.CreatedBy?.Handle ?? "?"}\n");
                sb.Append(p.Body).Append("\n\n");
            }
            _app.Invoke(() =>
            {
                _log.Text = sb.ToString();
                _log.MoveEnd();
                _log.SetNeedsDraw();
            });

            await UpsertReadAsync(db, DateTimeOffset.UtcNow);
        }
        catch (Exception ex)
        {
            _app.Invoke(() =>
            {
                _status.Text = $"[!] load failed: {ex.Message}";
                _status.SetScheme(BbsTheme.Warning);
            });
        }
    }

    private async Task PostReplyAsync(string body)
    {
        try
        {
            var now = DateTimeOffset.UtcNow;
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();

            db.Posts.Add(new Post
            {
                TopicId = _topic.Id,
                Body = body,
                CreatedById = _user.Id,
                CreatedAt = now,
            });

            // Bump the topic's last_post_at so it floats to the top of the topic list.
            var topic = await db.Topics.FirstAsync(t => t.Id == _topic.Id);
            topic.LastPostAt = now;

            await db.SaveChangesAsync();
            await UpsertReadAsync(db, now);

            _app.Invoke(() =>
            {
                var current = _log.Text ?? string.Empty;
                _log.Text = current + $"[{_user.FormatDateTime(now)}] {_user.Handle}\n{body}\n\n";
                _log.MoveEnd();
                _log.SetNeedsDraw();
            });
        }
        catch (Exception ex)
        {
            _app.Invoke(() =>
            {
                _status.Text = $"[!] post failed: {ex.Message}";
                _status.SetScheme(BbsTheme.Warning);
            });
        }
    }

    private async Task UpsertReadAsync(AppDbContext db, DateTimeOffset at)
    {
        var existing = await db.PostReads.FirstOrDefaultAsync(r => r.UserId == _user.Id && r.TopicId == _topic.Id);
        if (existing is null)
        {
            db.PostReads.Add(new PostRead { UserId = _user.Id, TopicId = _topic.Id, LastReadAt = at });
        }
        else
        {
            existing.LastReadAt = at;
        }
        await db.SaveChangesAsync();
    }
}
