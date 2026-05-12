using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Hosting;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui;

var builder = Host.CreateApplicationBuilder(args);

builder.AddServiceDefaults();

builder.AddNpgsqlDbContext<AppDbContext>("bbs", configureDbContextOptions: o => o.UseSnakeCaseNamingConvention());
builder.AddRedisClient("redis");

builder.Services.AddSingleton<AuthLookupService>();
builder.Services.AddSingleton<IRealtimeBus, RedisRealtimeBus>();
builder.Services.AddSingleton<ChatService>();
builder.Services.AddSingleton<ProfileService>();
builder.Services.AddSingleton<SysopBootstrap>();
builder.Services.AddSingleton<ArtProvider>();
builder.Services.AddSingleton<Night.Ms.SshServer.Tui.Art.IArtGalleryProvider, Night.Ms.SshServer.Tui.Art.FileSystemArtGalleryProvider>();

// Pluggable provider interfaces (M10 follow-up F9). Open-Meteo + Hacker News ship as the
// default no-key implementations; swap in a paid news source or a different weather API
// later by re-binding INewsProvider / IWeatherProvider.
builder.Services.AddHttpClient(OpenMeteoWeatherProvider.HttpClientName);
builder.Services.AddHttpClient(HackerNewsProvider.HttpClientName);
builder.Services.AddHttpClient(OpenMeteoGeocodingProvider.HttpClientName);
builder.Services.AddHttpClient(IpApiCoGeolocationProvider.HttpClientName);
builder.Services.AddSingleton<IWeatherProvider, OpenMeteoWeatherProvider>();
builder.Services.AddSingleton<INewsProvider, HackerNewsProvider>();
builder.Services.AddSingleton<IGeocodingProvider, OpenMeteoGeocodingProvider>();
builder.Services.AddSingleton<IIpGeolocationProvider, IpApiCoGeolocationProvider>();

// DatabaseInitializer must run before SysopBootstrap (the bootstrap needs the schema), and
// SysopBootstrap must run before SshHost so a re-promotion lands before the first login.
builder.Services.AddHostedService<DatabaseInitializer>();
builder.Services.AddHostedService(sp => sp.GetRequiredService<SysopBootstrap>());
builder.Services.AddHostedService<SshHost>();

var host = builder.Build();
host.Run();
