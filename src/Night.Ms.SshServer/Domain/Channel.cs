namespace Night.Ms.SshServer.Domain;

public sealed class Channel
{
    public long Id { get; set; }
    public required string Name { get; set; }
    public string? Topic { get; set; }
    public bool IsPrivate { get; set; }
    public long? CreatedById { get; set; }
    public DateTimeOffset CreatedAt { get; set; }

    public User? CreatedBy { get; set; }
    public List<ChannelMember> Members { get; set; } = [];
    public List<ChatMessage> Messages { get; set; } = [];
}

public sealed class ChannelMember
{
    public long ChannelId { get; set; }
    public long UserId { get; set; }
    public DateTimeOffset JoinedAt { get; set; }
    public string Role { get; set; } = "member";

    public Channel? Channel { get; set; }
    public User? User { get; set; }
}

public sealed class ChatMessage
{
    public long Id { get; set; }
    public long ChannelId { get; set; }
    public long UserId { get; set; }
    public required string Body { get; set; }
    public DateTimeOffset CreatedAt { get; set; }

    public Channel? Channel { get; set; }
    public User? User { get; set; }
}
