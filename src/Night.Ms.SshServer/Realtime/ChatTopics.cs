namespace Night.Ms.SshServer.Realtime;

public static class ChatTopics
{
    public static string Channel(long channelId) => $"chat:channel:{channelId}";
}

public sealed record ChatMessageDto(long Id, long ChannelId, long UserId, string Handle, string Body, DateTimeOffset CreatedAt);
