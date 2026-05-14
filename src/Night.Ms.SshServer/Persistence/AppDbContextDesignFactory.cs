using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Design;

namespace Night.Ms.SshServer.Persistence;

// Used only by `dotnet ef migrations add` / `dotnet ef database update` at design time.
// Honors ConnectionStrings__bbs if set (so commands work against the run.ps1 dev container
// on its non-default port without a second Postgres install); otherwise falls back to a
// localhost:5432 default.
internal sealed class AppDbContextDesignFactory : IDesignTimeDbContextFactory<AppDbContext>
{
    public AppDbContext CreateDbContext(string[] args)
    {
        var connectionString =
            Environment.GetEnvironmentVariable("ConnectionStrings__bbs")
            ?? "Host=localhost;Port=5432;Database=bbs;Username=postgres;Password=postgres";

        var options = new DbContextOptionsBuilder<AppDbContext>()
            .UseNpgsql(connectionString)
            .UseSnakeCaseNamingConvention()
            .Options;
        return new AppDbContext(options);
    }
}
