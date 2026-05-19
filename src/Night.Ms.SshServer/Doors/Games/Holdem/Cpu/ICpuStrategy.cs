namespace Night.Ms.SshServer.Doors.Games.Holdem.Cpu;

// Stateless: same strategy instance can sit on multiple seats; reads everything it needs
// from the table state. The engine asks "given this state, what action does this seat
// take?" and gets back a legal HoldemAction.
public interface ICpuStrategy
{
    HoldemAction Decide(HoldemTableState state, int seatIndex);
}
