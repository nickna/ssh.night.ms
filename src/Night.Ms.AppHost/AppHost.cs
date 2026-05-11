var builder = DistributedApplication.CreateBuilder(args);

var postgres = builder.AddPostgres("pg")
    .WithDataVolume()
    .WithPgAdmin();

var bbsDb = postgres.AddDatabase("bbs");

var redis = builder.AddRedis("redis");

// SSH listener port. We deliberately don't declare it as an Aspire endpoint:
// `WithEndpoint(scheme: "tcp", port: …)` causes Aspire's DCP launcher to bind the
// port too (for proxying), which collides with SshServer's own bind on the same
// port — connections land on DCP and time out during the SSH banner exchange.
// SshServer listens directly on the port from BBS_SSH_PORT; Aspire still manages
// its lifecycle, logs, and connection-string injection.
const int sshPort = 2223;

builder.AddProject<Projects.Night_Ms_SshServer>("ssh")
    .WithReference(bbsDb)
    .WithReference(redis)
    .WaitFor(bbsDb)
    .WaitFor(redis)
    .WithEnvironment("BBS_SSH_PORT", sshPort.ToString());

builder.Build().Run();
