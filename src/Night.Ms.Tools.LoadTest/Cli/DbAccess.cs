using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.Tools.LoadTest.Cli;

// Standalone AppDbContext builder for the tool. Honors ConnectionStrings__bbs (the same env
// var run.ps1 and AppDbContextDesignFactory both use) and falls back to the local dev default,
// then runs the value through ConnectionStrings.BuildBbs so pool defaults match the server's.
internal static class DbAccess
{
    public static AppDbContext Create()
    {
        var raw =
            Environment.GetEnvironmentVariable("ConnectionStrings__bbs")
            ?? "Host=localhost;Port=5432;Database=bbs;Username=postgres;Password=postgres";
        var connectionString = ConnectionStrings.BuildBbs(raw);
        var options = new DbContextOptionsBuilder<AppDbContext>()
            .UseNpgsql(connectionString)
            .UseSnakeCaseNamingConvention()
            .Options;
        return new AppDbContext(options);
    }
}
