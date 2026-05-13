using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Design;

namespace Night.Ms.SshServer.Persistence;

// Used only by `dotnet ef migrations add` / `dotnet ef database update` at design time.
// The runtime app reads ConnectionStrings:bbs from configuration (set by run.ps1 / appsettings);
// this hard-coded localhost connection only needs to point at any Postgres reachable for
// scaffolding, not the production database.
internal sealed class AppDbContextDesignFactory : IDesignTimeDbContextFactory<AppDbContext>
{
    public AppDbContext CreateDbContext(string[] args)
    {
        var options = new DbContextOptionsBuilder<AppDbContext>()
            .UseNpgsql("Host=localhost;Port=5432;Database=bbs;Username=postgres;Password=postgres")
            .UseSnakeCaseNamingConvention()
            .Options;
        return new AppDbContext(options);
    }
}
