using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;

namespace Night.Ms.SshServer.Pages.U;

// Public profile page at /u/{handle}. No [Authorize] — anyone with the URL can see what
// /finger shows in the TUI: handle, joined date, last-seen, bio, location, stats, avatar.
// Banned users 404 (don't leak existence). Unknown handles also 404. The avatar img tag
// points at the dedicated /u/{handle}/avatar endpoint defined in Program.cs.
public sealed class ProfileModel(ProfileService profile, NightMsOptions options, AppDbContext db) : PageModel
{
    public string Handle { get; set; } = "";
    public string? RealName { get; set; }
    public string? Location { get; set; }
    public string? Bio { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
    public DateTimeOffset? LastSeenAt { get; set; }
    public bool IsSysop { get; set; }
    public int ChatMessageCount { get; set; }
    public int TopicCount { get; set; }
    public int PostCount { get; set; }
    public long AvatarVersion { get; set; }
    public string SshHost { get; set; } = "localhost";
    public int SshPort { get; set; } = 22;

    public async Task<IActionResult> OnGetAsync(string handle)
    {
        // A banned user existing-vs-missing leak is closed by checking IsBanned alongside
        // the snapshot. ProfileService.GetByHandleAsync would happily return a banned user,
        // so we filter in the same query.
        var user = await db.Users.AsNoTracking().FirstOrDefaultAsync(u => u.Handle == handle && !u.IsBanned);
        if (user is null) return NotFound();

        var snap = await profile.GetByHandleAsync(user.Handle, HttpContext.RequestAborted);
        if (snap is null) return NotFound();

        Handle = snap.Handle;
        RealName = snap.RealName;
        Location = snap.Location;
        Bio = snap.Bio;
        CreatedAt = snap.CreatedAt;
        LastSeenAt = snap.LastSeenAt;
        IsSysop = snap.IsSysop;
        ChatMessageCount = snap.ChatMessageCount;
        TopicCount = snap.TopicCount;
        PostCount = snap.PostCount;
        AvatarVersion = snap.ProfilePictureUpdatedAt?.UtcTicks ?? 0;
        SshHost = options.SshHost ?? "localhost";
        SshPort = options.SshPortPublic ?? options.SshPort ?? 2222;
        return Page();
    }
}
