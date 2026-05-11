namespace Night.Ms.SshServer.Domain;

public sealed class SshKey
{
    public long Id { get; set; }
    public long UserId { get; set; }
    public required string KeyType { get; set; }
    public required byte[] PublicKeyBlob { get; set; }
    public required string Fingerprint { get; set; }
    public string? Label { get; set; }
    public DateTimeOffset AddedAt { get; set; }

    public User? User { get; set; }
}
