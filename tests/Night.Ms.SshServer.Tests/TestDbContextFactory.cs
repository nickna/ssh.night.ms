using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Tests;

// Adapter so tests can hand the per-test DbContextOptions directly to services that take
// IDbContextFactory<AppDbContext>. The fixture builds the options once per test from a
// freshly-migrated database; this just hands out new contexts against them.
internal sealed class TestDbContextFactory(DbContextOptions<AppDbContext> options) : IDbContextFactory<AppDbContext>
{
    public AppDbContext CreateDbContext() => new(options);
}
