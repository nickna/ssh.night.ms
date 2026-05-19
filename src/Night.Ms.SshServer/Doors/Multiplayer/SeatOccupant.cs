namespace Night.Ms.SshServer.Doors.Multiplayer;

// Who's holding a seat. Humans have a User row + wallet; CPUs are synthetic and never touch
// the ledger. Serialized into the Redis seat hash as JSON with a "kind" discriminator; the
// ledger silently skips entries with UserId == null at settle time.
public abstract record SeatOccupant(string Handle)
{
    public sealed record Human(long UserId, string Handle) : SeatOccupant(Handle);
    public sealed record Cpu(string PersonaId, string Handle) : SeatOccupant(Handle);

    // Helper for callers that want "userId or null" without a pattern match. Named distinct
    // from the Human record's positional UserId parameter so the compiler doesn't see a
    // member-vs-parameter collision in the base.
    public long? AsHumanUserId() => this is Human h ? h.UserId : null;
}
