namespace Night.Ms.SshServer.Auth;

// Tracks "Never for this key" dismissals of the post-login adopt-key prompt. Redis-backed
// with a long TTL (NightMsOptions.KeyAdoption.DismissalTtlDays) so a user who declines
// today will eventually be re-prompted — useful if they later rotate to the offered key
// for real and come back. The TUI consults this before showing the modal.
public interface IDismissedKeyStore
{
    Task<bool> IsDismissedAsync(long userId, string fingerprint, CancellationToken ct);
    Task DismissAsync(long userId, string fingerprint, CancellationToken ct);
}
