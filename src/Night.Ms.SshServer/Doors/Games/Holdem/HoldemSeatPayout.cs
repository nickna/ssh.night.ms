namespace Night.Ms.SshServer.Doors.Games.Holdem;

// Gross chips a seat won (not net P&L). One row per pot won; a seat can appear multiple
// times if they took the main pot + a side pot. Caller computes net = sum(Payouts) -
// TotalContribution per seat for the ledger.
public sealed record HoldemSeatPayout(int SeatIndex, long Amount, string Reason);
