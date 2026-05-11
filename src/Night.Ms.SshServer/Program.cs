using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Hosting;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui;

var builder = Host.CreateApplicationBuilder(args);

builder.AddServiceDefaults();

builder.AddNpgsqlDbContext<AppDbContext>("bbs", configureDbContextOptions: o => o.UseSnakeCaseNamingConvention());
builder.AddRedisClient("redis");

builder.Services.AddSingleton<AuthLookupService>();
builder.Services.AddSingleton<IRealtimeBus, RedisRealtimeBus>();
builder.Services.AddSingleton<ChatService>();
builder.Services.AddSingleton<SysopBootstrap>();
builder.Services.AddSingleton<LoginArtProvider>();

// DatabaseInitializer must run before SysopBootstrap (the bootstrap needs the schema), and
// SysopBootstrap must run before SshHost so a re-promotion lands before the first login.
builder.Services.AddHostedService<DatabaseInitializer>();
builder.Services.AddHostedService(sp => sp.GetRequiredService<SysopBootstrap>());
builder.Services.AddHostedService<SshHost>();

var host = builder.Build();
host.Run();
