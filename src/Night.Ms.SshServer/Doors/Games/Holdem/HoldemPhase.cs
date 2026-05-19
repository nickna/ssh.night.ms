namespace Night.Ms.SshServer.Doors.Games.Holdem;

public enum HoldemPhase
{
    Idle,           // between hands
    PreFlop,
    Flop,
    Turn,
    River,
    Showdown,
    HandComplete,   // payouts populated; coordinator may settle and start a new hand
}
