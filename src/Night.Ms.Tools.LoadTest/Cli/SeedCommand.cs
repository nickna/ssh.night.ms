using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.Tools.LoadTest.Bots;

namespace Night.Ms.Tools.LoadTest.Cli;

// `seed --count N [--keys-dir ./loadtest-keys]`
// Idempotent: re-running with the same count is a no-op past the first run. Adding 100 to a
// prior run of 50 inserts only the new 50.
internal static class SeedCommand
{
    public static async Task<int> RunAsync(int count, string keysDir, CancellationToken ct)
    {
        if (count <= 0)
        {
            Console.Error.WriteLine("loadtest seed: --count must be positive.");
            return 1;
        }

        var store = new BotKeyStore(keysDir);
        await using var db = DbAccess.Create();

        // Pre-load existing handles + credentials in one shot rather than N round-trips.
        var handles = Enumerable.Range(1, count).Select(store.HandleFor).ToList();
        var existingUsers = await db.Users
            .Where(u => handles.Contains(u.Handle))
            .Select(u => new { u.Id, u.Handle })
            .ToListAsync(ct);
        var existingByHandle = existingUsers.ToDictionary(u => u.Handle, u => u.Id, StringComparer.OrdinalIgnoreCase);

        var now = DateTimeOffset.UtcNow;
        var inserted = 0;
        var alreadyPresent = 0;

        for (var i = 1; i <= count; i++)
        {
            ct.ThrowIfCancellationRequested();
            var handle = store.HandleFor(i);
            var key = store.LoadOrGenerate(i);

            long userId;
            if (existingByHandle.TryGetValue(handle, out var existingId))
            {
                userId = existingId;
            }
            else
            {
                var user = new User { Handle = handle, CreatedAt = now };
                db.Users.Add(user);
                await db.SaveChangesAsync(ct);
                userId = user.Id;
                inserted++;
            }

            // Credential upsert keyed on (Provider, Subject) — same uniqueness the server enforces.
            var fp = key.Fingerprint;
            var hasCred = await db.IdentityCredentials.AnyAsync(
                c => c.Provider == CredentialProvider.Ssh && c.Subject == fp, ct);
            if (!hasCred)
            {
                var metadata = JsonSerializer.Serialize(new
                {
                    algorithm = key.Algorithm,
                    blob_b64 = Convert.ToBase64String(key.PublicKeyBlob),
                });
                db.IdentityCredentials.Add(new IdentityCredential
                {
                    UserId = userId,
                    Provider = CredentialProvider.Ssh,
                    Subject = fp,
                    Metadata = metadata,
                    Label = "loadtest",
                    CreatedAt = now,
                });
                await db.SaveChangesAsync(ct);
            }
            else
            {
                alreadyPresent++;
            }

            if (i % 100 == 0)
            {
                Console.Out.WriteLine($"loadtest seed: {i}/{count}...");
            }
        }

        Console.Out.WriteLine($"loadtest seed: done. inserted={inserted} already-present={alreadyPresent} keys-dir={keysDir}");
        return 0;
    }
}
