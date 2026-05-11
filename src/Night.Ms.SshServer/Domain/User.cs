namespace Night.Ms.SshServer.Domain;

public sealed class User
{
    public long Id { get; set; }
    public required string Handle { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
    public DateTimeOffset? LastSeenAt { get; set; }
    public bool IsSysop { get; set; }
    public bool IsBanned { get; set; }

    public List<SshKey> Keys { get; set; } = [];
}
