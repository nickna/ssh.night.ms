namespace Night.Ms.SshServer.Doors.Games.Holdem;

// One layer of the pot. Multiple pots arise when 2+ seats go all-in for different amounts:
// the smallest contribution forms the main pot (everyone still in is eligible), the next
// level forms a side pot from which the smallest all-in is excluded, and so on.
public sealed record HoldemPot(long Amount, IReadOnlySet<int> EligibleSeats);
