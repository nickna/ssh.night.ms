using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public sealed class NewTopicScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly AppDbContext _db;
    private readonly User _user;
    private readonly Forum _forum;
    private readonly TextField _title;
    private readonly TextView _body;
    private readonly Label _status;

    public NewTopicScreen(IApplication app, IServiceProvider services, AppDbContext db, User user, Forum forum)
        : base(app, services, user)
    {
        _app = app;
        _db = db;
        _user = user;
        _forum = forum;
        Title = $"new topic in #{forum.Name} — [Ctrl+S] submit — [Esc] cancel";

        var titleLabel = new Label { X = 2, Y = 1, Text = "Title:" };
        titleLabel.SetScheme(BbsTheme.Hint);
        Add(titleLabel);
        _title = new TextField { X = 2, Y = 2, Width = Dim.Fill(2) };
        _title.SetScheme(BbsTheme.Input);

        var bodyLabel = new Label { X = 2, Y = 4, Text = "Body:" };
        bodyLabel.SetScheme(BbsTheme.Hint);
        Add(bodyLabel);
        _body = new TextView
        {
            X = 2,
            Y = 5,
            Width = Dim.Fill(2),
            Height = Dim.Fill(4),
        };
        _body.SetScheme(BbsTheme.Input);

        _status = new Label
        {
            X = 2,
            Y = Pos.Bottom(_body),
            Width = Dim.Fill(2),
        };
        _status.SetScheme(BbsTheme.Status);

        var submit = new Button
        {
            X = 2,
            Y = Pos.Bottom(_status),
            Text = "Submit",
            IsDefault = true,
        };
        submit.Accepting += (_, e) =>
        {
            e.Handled = true;
            _ = SubmitAsync();
        };

        var cancel = new Button
        {
            X = Pos.Right(submit) + 2,
            Y = Pos.Bottom(_status),
            Text = "Cancel",
        };
        cancel.Accepting += (_, e) =>
        {
            e.Handled = true;
            Result = null;
            _app.RequestStop();
        };

        Add(_title, _body, _status, submit, cancel);
        _title.SetFocus();

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                Result = null;
                _app.RequestStop();
                key.Handled = true;
            }
            else if (key == Key.S.WithCtrl)
            {
                _ = SubmitAsync();
                key.Handled = true;
            }
        };
    }

    private async Task SubmitAsync()
    {
        var title = (_title.Text ?? string.Empty).Trim();
        var body = (_body.Text ?? string.Empty).Trim();
        if (title.Length is < 3 or > 120)
        {
            SetStatusError("[!] Title must be 3–120 characters.");
            return;
        }
        if (body.Length < 1)
        {
            SetStatusError("[!] Body cannot be empty.");
            return;
        }

        try
        {
            var now = DateTimeOffset.UtcNow;
            var topic = new Topic
            {
                ForumId = _forum.Id,
                Title = title,
                CreatedById = _user.Id,
                CreatedAt = now,
                LastPostAt = now,
            };
            _db.Topics.Add(topic);
            await _db.SaveChangesAsync();

            _db.Posts.Add(new Post
            {
                TopicId = topic.Id,
                Body = body,
                CreatedById = _user.Id,
                CreatedAt = now,
            });
            _db.PostReads.Add(new PostRead
            {
                UserId = _user.Id,
                TopicId = topic.Id,
                LastReadAt = now,
            });
            await _db.SaveChangesAsync();

            Result = topic;
            _app.RequestStop();
        }
        catch (Exception ex)
        {
            SetStatusError($"[!] Failed to create topic: {ex.Message}");
        }
    }

    private void SetStatusError(string text)
    {
        _status.Text = text;
        _status.SetScheme(BbsTheme.Warning);
    }
}
