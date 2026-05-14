using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Hosting;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Reader;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui;
using Night.Ms.SshServer.Tui.Map;
using StackExchange.Redis;

var builder = Host.CreateApplicationBuilder(args);

// Single typed view of the NIGHTMS_* env vars + their appsettings aliases. Bound once at
// boot so consumers don't each repeat the env-or-config fallback lookup.
builder.Services.AddSingleton(NightMsOptions.FromConfiguration(builder.Configuration));

// Postgres + EF Core. Connection string lives under ConnectionStrings:bbs (set by
// run.ps1, or via appsettings in production). Snake-case convention matches the
// existing migrations.
//
// Register both:
//  - AddDbContext (scoped) — for hosted services + screens that open their own scope
//    (DatabaseInitializer, NewTopicScreen, etc.).
//  - AddDbContextFactory (singleton) — for stateless realtime services that previously
//    opened a fresh scope on every public method just to resolve AppDbContext.
//
// The factory must be a singleton (it has no scope to live in) — which means the shared
// DbContextOptions<AppDbContext> must also be Singleton. The optionsLifetime arg below
// bumps it from the default Scoped to Singleton; runtime DI validation rejects the dual
// registration otherwise.
void ConfigureDb(DbContextOptionsBuilder opt) =>
    opt.UseNpgsql(builder.Configuration.GetConnectionString("bbs"))
       .UseSnakeCaseNamingConvention();
builder.Services.AddDbContext<AppDbContext>(ConfigureDb,
    contextLifetime: ServiceLifetime.Scoped,
    optionsLifetime: ServiceLifetime.Singleton);
builder.Services.AddDbContextFactory<AppDbContext>(ConfigureDb, lifetime: ServiceLifetime.Singleton);

// Redis. ConnectionMultiplexer is thread-safe and intended to be shared as a
// singleton across the whole app.
builder.Services.AddSingleton<IConnectionMultiplexer>(_ =>
    ConnectionMultiplexer.Connect(builder.Configuration.GetConnectionString("redis")
        ?? throw new InvalidOperationException("ConnectionStrings:redis is not configured.")));

builder.Services.AddSingleton<AuthLookupService>();
builder.Services.AddSingleton<IRealtimeBus, RedisRealtimeBus>();
builder.Services.AddSingleton<ChatService>();
builder.Services.AddSingleton<ChatMutationService>();
builder.Services.AddSingleton<PresenceService>();
builder.Services.AddSingleton<ReadStateService>();
builder.Services.AddSingleton<ProfileService>();
builder.Services.AddSingleton<SysopBootstrap>();
builder.Services.AddSingleton<ArtProvider>();
builder.Services.AddSingleton<Night.Ms.SshServer.Tui.Art.IArtGalleryProvider, Night.Ms.SshServer.Tui.Art.FileSystemArtGalleryProvider>();

// Pluggable provider interfaces (M10 follow-up F9). Open-Meteo + Hacker News ship as the
// default no-key implementations; swap in a paid news source or a different weather API by
// re-binding the relevant interface after these calls. Each AddXxx method owns its named
// HttpClient (BaseAddress, User-Agent) so swapping a provider doesn't leak the prior
// vendor's URL into Program.cs.
builder.Services.AddOpenMeteoWeather();
builder.Services.AddHackerNews();
builder.Services.AddOpenMeteoGeocoding();
builder.Services.AddIpApiCoGeolocation();
builder.Services.AddSmartReaderArticleReader();
builder.Services.AddHttpImageFetcher();
builder.Services.AddOsmTileFetcher();
builder.Services.AddOpenFreeMapVectorTileFetcher();

// DatabaseInitializer must run before SysopBootstrap (the bootstrap needs the schema), and
// SysopBootstrap must run before SshHost so a re-promotion lands before the first login.
builder.Services.AddHostedService<DatabaseInitializer>();
builder.Services.AddHostedService(sp => sp.GetRequiredService<SysopBootstrap>());
builder.Services.AddHostedService<SshHost>();

var host = builder.Build();
host.Run();
