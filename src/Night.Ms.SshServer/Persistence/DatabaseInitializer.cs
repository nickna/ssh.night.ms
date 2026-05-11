using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;

namespace Night.Ms.SshServer.Persistence;

// Hosted service that runs once at startup: applies pending migrations and seeds the
// default #lobby channel + General forum if they don't exist. Runs before SshHost
// thanks to the order services are added to the container.
public sealed class DatabaseInitializer(
    IServiceProvider services,
    ILogger<DatabaseInitializer> logger) : IHostedService
{
    public async Task StartAsync(CancellationToken cancellationToken)
    {
        using var scope = services.CreateScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();

        logger.LogInformation("Applying pending migrations...");
        await db.Database.MigrateAsync(cancellationToken);

        await SeedAsync(db, cancellationToken);
        logger.LogInformation("Database ready.");
    }

    public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;

    private async Task SeedAsync(AppDbContext db, CancellationToken cancellationToken)
    {
        var now = DateTimeOffset.UtcNow;

        if (!await db.Channels.AnyAsync(c => c.Name == "lobby", cancellationToken))
        {
            db.Channels.Add(new Channel
            {
                Name = "lobby",
                Topic = "Welcome to ssh.night.ms — please mind the cables.",
                IsPrivate = false,
                CreatedAt = now,
            });
            logger.LogInformation("Seeded default channel: #lobby");
        }

        if (!await db.Forums.AnyAsync(f => f.Name == "General", cancellationToken))
        {
            db.Forums.Add(new Forum
            {
                Name = "General",
                Description = "Everything that doesn't fit anywhere else.",
                SortOrder = 0,
            });
            logger.LogInformation("Seeded default forum: General");
        }

        await db.SaveChangesAsync(cancellationToken);
    }
}
