namespace Night.Ms.SshTransport;

public sealed record AuthQuery(string Fingerprint, string KeyAlgorithm, byte[] PublicKeyBlob, string? Username);

public abstract record AuthDecision
{
    public sealed record Known(long UserId, string Handle, bool IsSysop) : AuthDecision;

    public sealed record Unknown : AuthDecision
    {
        public static readonly Unknown Instance = new();
    }

    public sealed record Banned(string Reason) : AuthDecision;
}
