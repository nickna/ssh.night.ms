var builder = DistributedApplication.CreateBuilder(args);

var postgres = builder.AddPostgres("pg")
    .WithDataVolume()
    .WithPgAdmin();

var bbsDb = postgres.AddDatabase("bbs");

var redis = builder.AddRedis("redis");

builder.AddProject<Projects.Night_Ms_SshServer>("ssh")
    .WithReference(bbsDb)
    .WithReference(redis)
    .WaitFor(bbsDb)
    .WaitFor(redis)
    .WithEndpoint(name: "ssh-listener", scheme: "tcp", port: 2222, targetPort: 2222);

builder.Build().Run();
