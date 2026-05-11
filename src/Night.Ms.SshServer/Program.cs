using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Hosting;
using Night.Ms.SshServer.Persistence;

var builder = Host.CreateApplicationBuilder(args);

builder.AddServiceDefaults();

builder.AddNpgsqlDbContext<AppDbContext>("bbs", configureDbContextOptions: o => o.UseSnakeCaseNamingConvention());
builder.AddRedisClient("redis");

builder.Services.AddSingleton<AuthLookupService>();

// DatabaseInitializer must run before SshHost so the schema and seed data are ready.
builder.Services.AddHostedService<DatabaseInitializer>();
builder.Services.AddHostedService<SshHost>();

var host = builder.Build();
host.Run();
