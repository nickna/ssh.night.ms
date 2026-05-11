using System.Text.Json;

namespace Night.Ms.SshServer.Domain;

public sealed class AuditLog
{
    public long Id { get; set; }
    public long? ActorId { get; set; }
    public required string Action { get; set; }
    public required string TargetType { get; set; }
    public long? TargetId { get; set; }
    public JsonDocument? Details { get; set; }
    public DateTimeOffset CreatedAt { get; set; }

    public User? Actor { get; set; }
}
