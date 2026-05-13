using System.Net;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Hosting;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Reader;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui;
using StackExchange.Redis;

var builder = Host.CreateApplicationBuilder(args);

// Postgres + EF Core. Connection string lives under ConnectionStrings:bbs (set by
// run.ps1, or via appsettings in production). Snake-case convention matches the
// existing migrations.
//
// Register both:
//  - AddDbContext (scoped) — for hosted services + screens that open their own scope
//    (DatabaseInitializer, NewTopicScreen, etc.).
//  - AddDbContextFactory (singleton) — for stateless realtime services that previously
//    opened a fresh scope on every public method just to resolve AppDbContext.
void ConfigureDb(DbContextOptionsBuilder opt) =>
    opt.UseNpgsql(builder.Configuration.GetConnectionString("bbs"))
       .UseSnakeCaseNamingConvention();
builder.Services.AddDbContext<AppDbContext>(ConfigureDb);
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
// default no-key implementations; swap in a paid news source or a different weather API
// later by re-binding INewsProvider / IWeatherProvider.
// BaseAddress is set up-front so providers don't race to lazily assign it on the shared
// HttpClient instance handed out by IHttpClientFactory.
builder.Services.AddHttpClient(OpenMeteoWeatherProvider.HttpClientName, c =>
    c.BaseAddress = new Uri("https://api.open-meteo.com/"));
builder.Services.AddHttpClient(HackerNewsProvider.HttpClientName, c =>
    c.BaseAddress = new Uri("https://hacker-news.firebaseio.com/"));
builder.Services.AddHttpClient(OpenMeteoGeocodingProvider.HttpClientName, c =>
    c.BaseAddress = new Uri("https://geocoding-api.open-meteo.com/"));
builder.Services.AddHttpClient(IpApiCoGeolocationProvider.HttpClientName, c =>
    c.BaseAddress = new Uri("https://ipapi.co/"));
builder.Services.AddHttpClient(SmartReaderArticleReader.HttpClientName, c =>
{
    // Identify ourselves so site operators can trace traffic back to a known BBS rather
    // than seeing an unbranded HttpClient. AutoRedirect is on by default — articles often
    // sit behind one or two 301 hops.
    c.DefaultRequestHeaders.UserAgent.ParseAdd("ssh.night.ms-reader/0.1 (+https://night.ms)");
    c.DefaultRequestHeaders.Accept.ParseAdd("text/html,application/xhtml+xml;q=0.9,*/*;q=0.5");
});
builder.Services.AddHttpClient(HttpImageFetcher.HttpClientName, c =>
{
    c.DefaultRequestHeaders.UserAgent.ParseAdd("ssh.night.ms-reader/0.1 (+https://night.ms)");
    c.DefaultRequestHeaders.Accept.ParseAdd("image/*");
});
builder.Services.AddHttpClient(Night.Ms.SshServer.Tui.Map.OsmTileFetcher.HttpClientName, c =>
{
    // OSM Tile Usage Policy requires a project-identifying User-Agent. Without one, the
    // tile server may rate-limit or block; with one, sysadmins can reach us if our usage
    // becomes a problem. See https://operations.osmfoundation.org/policies/tiles/.
    c.DefaultRequestHeaders.UserAgent.ParseAdd("ssh.night.ms-map/0.1 (+https://night.ms; contact=nick@night.ms)");
    c.DefaultRequestHeaders.Accept.ParseAdd("image/png,image/*;q=0.8");
});
builder.Services.AddHttpClient(Night.Ms.SshServer.Tui.Map.OpenFreeMapVectorTileFetcher.HttpClientName, c =>
{
    c.DefaultRequestHeaders.UserAgent.ParseAdd("ssh.night.ms-map/0.1 (+https://night.ms; contact=nick@night.ms)");
    c.DefaultRequestHeaders.Accept.ParseAdd("application/vnd.mapbox-vector-tile,application/x-protobuf;q=0.9,application/json;q=0.5");
}).ConfigurePrimaryHttpMessageHandler(() => new HttpClientHandler
{
    // OpenFreeMap serves .pbf with Content-Encoding: gzip by default; HttpClient won't
    // transparently decompress unless we opt in here. Brotli covers the TileJSON manifest.
    AutomaticDecompression = DecompressionMethods.GZip | DecompressionMethods.Brotli,
});
builder.Services.AddSingleton<IWeatherProvider, OpenMeteoWeatherProvider>();
builder.Services.AddSingleton<INewsProvider, HackerNewsProvider>();
builder.Services.AddSingleton<IGeocodingProvider, OpenMeteoGeocodingProvider>();
builder.Services.AddSingleton<IIpGeolocationProvider, IpApiCoGeolocationProvider>();
builder.Services.AddSingleton<IArticleReader, SmartReaderArticleReader>();
builder.Services.AddSingleton<IImageFetcher, HttpImageFetcher>();
builder.Services.AddSingleton<Night.Ms.SshServer.Tui.Map.IOsmTileFetcher, Night.Ms.SshServer.Tui.Map.OsmTileFetcher>();
builder.Services.AddSingleton<Night.Ms.SshServer.Tui.Map.IVectorTileFetcher, Night.Ms.SshServer.Tui.Map.OpenFreeMapVectorTileFetcher>();

// DatabaseInitializer must run before SysopBootstrap (the bootstrap needs the schema), and
// SysopBootstrap must run before SshHost so a re-promotion lands before the first login.
builder.Services.AddHostedService<DatabaseInitializer>();
builder.Services.AddHostedService(sp => sp.GetRequiredService<SysopBootstrap>());
builder.Services.AddHostedService<SshHost>();

var host = builder.Build();
host.Run();
