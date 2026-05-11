using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Persistence;
using Testcontainers.PostgreSql;

namespace Night.Ms.SshServer.Tests;

// Spins up a real Postgres container once per test class. Each test gets a fresh DB created
// from migrations so data from one test can't leak into the next without explicit setup.
public sealed class PostgresFixture : IAsyncLifetime
{
    private readonly PostgreSqlContainer _container = new PostgreSqlBuilder("postgres:17-alpine")
        .WithDatabase("bbs_template")
        .WithUsername("postgres")
        .WithPassword("postgres")
        .Build();

    public string ConnectionString { get; private set; } = string.Empty;

    public async Task InitializeAsync()
    {
        await _container.StartAsync();
        ConnectionString = _container.GetConnectionString();

        // Apply migrations once against the template DB. Each test then clones from this.
        var options = new DbContextOptionsBuilder<AppDbContext>()
            .UseNpgsql(ConnectionString)
            .UseSnakeCaseNamingConvention()
            .Options;
        await using var db = new AppDbContext(options);
        await db.Database.MigrateAsync();
    }

    // Builds a DbContextOptions that points at a freshly created per-test database. Each
    // test class instantiates one of these in its constructor so tests don't share state.
    public async Task<DbContextOptions<AppDbContext>> CreateFreshDatabaseAsync()
    {
        var dbName = $"bbs_test_{Guid.NewGuid():N}";

        // Drop+create through a connection to the default 'postgres' database.
        var adminCs = new Npgsql.NpgsqlConnectionStringBuilder(ConnectionString) { Database = "postgres" }.ToString();
        await using var admin = new Npgsql.NpgsqlConnection(adminCs);
        await admin.OpenAsync();
        await using (var cmd = admin.CreateCommand())
        {
            cmd.CommandText = $"CREATE DATABASE \"{dbName}\";";
            await cmd.ExecuteNonQueryAsync();
        }

        var testCs = new Npgsql.NpgsqlConnectionStringBuilder(ConnectionString) { Database = dbName }.ToString();
        var options = new DbContextOptionsBuilder<AppDbContext>()
            .UseNpgsql(testCs)
            .UseSnakeCaseNamingConvention()
            .Options;
        await using var db = new AppDbContext(options);
        await db.Database.MigrateAsync();
        return options;
    }

    public async Task DisposeAsync() => await _container.DisposeAsync();
}
