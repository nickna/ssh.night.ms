using Night.Ms.SshServer.Hosting;

var builder = Host.CreateApplicationBuilder(args);

builder.AddServiceDefaults();

builder.AddNpgsqlDataSource("bbs");
builder.AddRedisClient("redis");

builder.Services.AddHostedService<SshHost>();

var host = builder.Build();
host.Run();
