using System.Security.Claims;
using Microsoft.AspNetCore.Authorization;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Pages;

[Authorize]
public sealed class ProfileModel(AppDbContext db, NightMsOptions options) : PageModel
{
    public string Handle { get; set; } = "";
    public string? Email { get; set; }
    public bool IsSysop { get; set; }
    public bool HasProfilePicture { get; set; }
    // Cache-bust value the avatar <img> appends as ?v=… so a fresh upload reloads instantly.
    // Ticks-from-UTC when set, "0" otherwise (identicon).
    public long AvatarVersion { get; set; }
    public List<CredentialRow> Credentials { get; set; } = new();
    public bool CanLinkGoogle { get; private set; }
    public bool CanLinkMicrosoft { get; private set; }
    public string? Flash { get; set; }

    public async Task<IActionResult> OnGetAsync([FromQuery] string? flash)
    {
        var idStr = User.FindFirstValue(ClaimTypes.NameIdentifier);
        if (!long.TryParse(idStr, out var userId)) return Redirect("/login");

        var user = await db.Users
            .Include(u => u.Credentials)
            .FirstOrDefaultAsync(u => u.Id == userId);
        if (user is null) return Redirect("/login");

        Handle = user.Handle;
        Email = user.Email;
        IsSysop = user.IsSysop;
        HasProfilePicture = user.ProfilePictureUpdatedAt is not null;
        AvatarVersion = user.ProfilePictureUpdatedAt?.UtcTicks ?? 0;
        Flash = flash;
        Credentials = user.Credentials
            .OrderBy(c => c.Provider).ThenBy(c => c.CreatedAt)
            .Select(c => new CredentialRow(
                c.Id,
                c.Provider.ToString(),
                ShortenSubject(c.Subject),
                c.CreatedAt,
                c.LastUsedAt))
            .ToList();

        var linked = user.Credentials.Select(c => c.Provider).ToHashSet();
        CanLinkGoogle = options.IsGoogleConfigured && !linked.Contains(CredentialProvider.Google);
        CanLinkMicrosoft = options.IsMicrosoftConfigured && !linked.Contains(CredentialProvider.Microsoft);
        return Page();
    }

    // Visual: SSH fingerprints are long (SHA256:base64...); OIDC sub claims are opaque IDs.
    // We show the first 12 chars + ellipsis for non-SSH; SSH subjects are already prefixed
    // with the hash algorithm so we keep the prefix + last 8 chars.
    private static string ShortenSubject(string subject)
    {
        if (subject.Length <= 24) return subject;
        if (subject.StartsWith("SHA256:", StringComparison.Ordinal))
        {
            return $"{subject[..15]}…{subject[^6..]}";
        }
        return $"{subject[..12]}…";
    }

    public sealed record CredentialRow(long Id, string Provider, string SubjectShort, DateTimeOffset CreatedAt, DateTimeOffset? LastUsedAt);
}
