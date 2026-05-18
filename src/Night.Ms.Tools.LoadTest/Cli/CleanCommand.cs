using Microsoft.EntityFrameworkCore;

namespace Night.Ms.Tools.LoadTest.Cli;

// `clean` — deletes every user whose handle matches `loadbot-*`. The Users → IdentityCredentials
// foreign key is OnDelete=Cascade, so credentials disappear with their owners. Anything the bot
// authored (chat messages, posts, topics) uses Restrict, so those need to be wiped by hand or
// by `run.ps1 -Reset` if you want a truly clean DB.
internal static class CleanCommand
{
    public static async Task<int> RunAsync(CancellationToken ct)
    {
        await using var db = DbAccess.Create();
        // citext column makes the LIKE case-insensitive in Postgres regardless of casing here.
        var deleted = await db.Users.Where(u => EF.Functions.Like(u.Handle, "loadbot-%")).ExecuteDeleteAsync(ct);
        Console.Out.WriteLine($"loadtest clean: deleted {deleted} loadbot-* users.");
        return 0;
    }
}
