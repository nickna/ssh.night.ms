namespace Night.Ms.SshServer.Doors.Multiplayer;

// Tops a table up with CPUs to a minimum seat floor. Called by the coordinator after every
// settle and every leave. The filler picks a persona from the registry that isn't currently
// seated at the table, materializes a CPU seat with a starting chip stack, and writes it
// into the seats hash. It does NOT touch the ledger — CPUs have no wallets.
public interface ICpuFiller
{
    Task EnsureMinimumAsync(string gameKey, long tableId, int minSeats, long startingChips, CancellationToken ct);
}
