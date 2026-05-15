using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Night.Ms.SshServer.Web;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Modal screen shown when a user runs `/finger <handle>` from chat. The classic Unix
// finger output is plain text, but in this BBS we want the subject's profile picture front
// and center — so we open a dedicated screen with the avatar rendered as half-block art on
// the left and the same fields FormatFinger prints on the right. Esc returns to chat; the
// screen never edits anything and never persists state.
public sealed class FingerScreen : BbsWindow
{
    private const int AvatarCols = 30;

    public FingerScreen(IApplication app, IServiceProvider services, User viewer, ProfileSnapshot subject)
        : base(app, services, viewer)
    {
        Title = $"finger — {subject.Handle} — [Esc] back";

        // Avatar pane (left column).
        var avatar = new AnsiArtView
        {
            X = 2,
            Y = 1,
            // Height/Width auto-size from the CellGrid once it's set; the view doesn't reserve
            // space until then. We leave 30 cols for the half-block render and ~15 rows of
            // vertical room (FingerScreen is sized so this fits even on an 80×24 terminal).
        };
        Add(avatar);

        // Text pane (right column). One label per visible line, mirroring FormatFinger's
        // layout but rendered as individual views so the avatar can sit next to them.
        const int textCol = AvatarCols + 4; // 30 cols of avatar + 2 of padding + 2 of margin
        var lineY = 1;
        var headerText = subject.IsSysop
            ? $"── finger {subject.Handle} (sysop) ──"
            : $"── finger {subject.Handle} ──";
        AddLine(headerText, textCol, lineY++, BbsTheme.Header_);
        lineY++; // blank line for breathing room

        AddLine($"joined     {viewer.FormatDate(subject.CreatedAt)}", textCol, lineY++);
        AddLine($"last seen  {(subject.LastSeenAt is { } ls ? viewer.FormatDateTime(ls) : "<never>")}", textCol, lineY++);
        if (!string.IsNullOrEmpty(subject.RealName))
            AddLine($"real name  {subject.RealName}", textCol, lineY++);
        if (!string.IsNullOrEmpty(subject.Location))
            AddLine($"location   {subject.Location}", textCol, lineY++);
        if (!string.IsNullOrEmpty(subject.Bio))
            AddWrappedBio(subject.Bio, textCol, ref lineY);
        AddLine($"stats      {subject.ChatMessageCount} chat / {subject.TopicCount} topics / {subject.PostCount} posts",
            textCol, lineY++);

        // Kick off the avatar render in the background and push the resulting grid onto
        // the view via Application.Invoke. Same pattern as ProfileEditScreen.
        LoadAvatarAsync(app, services, subject, AvatarCols, avatar);

        InstallEscapeHandler();
    }

    private void AddLine(string text, int x, int y, Terminal.Gui.Drawing.Scheme? scheme = null)
    {
        var label = new Label { X = x, Y = y, Text = text };
        if (scheme is not null) label.SetScheme(scheme);
        else label.SetScheme(BbsTheme.Hint);
        Add(label);
    }

    // Wraps a bio at ~40 cols so it doesn't overflow the 60-col text pane. Each wrapped line
    // gets its own Label; the first prefixed with "bio        ", the rest indented to match.
    private void AddWrappedBio(string bio, int x, ref int y)
    {
        const int width = 40;
        const string firstPrefix = "bio        ";
        var indent = new string(' ', firstPrefix.Length);
        var words = bio.Split(' ');
        var current = "";
        var firstLine = true;
        foreach (var w in words)
        {
            var candidate = current.Length == 0 ? w : current + " " + w;
            if (candidate.Length > width)
            {
                AddLine((firstLine ? firstPrefix : indent) + current, x, y++);
                firstLine = false;
                current = w;
            }
            else
            {
                current = candidate;
            }
        }
        if (current.Length > 0)
        {
            AddLine((firstLine ? firstPrefix : indent) + current, x, y++);
        }
    }

    private static void LoadAvatarAsync(IApplication app, IServiceProvider services, ProfileSnapshot subject, int cols, AnsiArtView view)
    {
        Task.Run(async () =>
        {
            try
            {
                using var scope = services.CreateScope();
                var pfp = scope.ServiceProvider.GetRequiredService<ProfilePictureService>();
                var grid = await pfp.GetCellGridAsync(subject.UserId, subject.Handle, cols, subject.ProfilePictureUpdatedAt, default);
                app.Invoke(() =>
                {
                    view.Grid = grid;
                    view.SetNeedsDraw();
                });
            }
            catch
            {
                // Bad image shouldn't make /finger fail; the screen still shows the text fields.
            }
        });
    }
}
