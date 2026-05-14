namespace Night.Ms.SshServer.Domain;

public enum CredentialProvider
{
    Ssh = 0,
    Google = 1,
    Microsoft = 2,
}

// A single credential that authenticates a User. One user owns N credentials of any kind:
// SSH keys (Subject = fingerprint), Google or Microsoft OIDC (Subject = the provider's
// stable subject claim). Lookup is keyed on (Provider, Subject) — unique, case-sensitive.
// Metadata is a jsonb blob whose shape depends on the provider:
//   ssh:       { "algorithm": "ssh-ed25519", "blob_b64": "..." }
//   google:    { "email": "...", "email_verified": true, "name": "..." }
//   microsoft: { "email": "...", "email_verified": true, "name": "..." }
// Storing it loosely keeps the schema stable when a provider adds a new claim we want to
// stash; queries that need the email use User.Email instead.
public sealed class IdentityCredential
{
    public long Id { get; set; }
    public long UserId { get; set; }
    public CredentialProvider Provider { get; set; }
    public required string Subject { get; set; }
    public string? Metadata { get; set; }
    public string? Label { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
    public DateTimeOffset? LastUsedAt { get; set; }

    public User? User { get; set; }
}
