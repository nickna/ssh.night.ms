namespace Night.Ms.SshServer.Domain;

// Per-user coin wallet for door games. Two buckets, semantically distinct:
//   DailyCredits — refreshed to a fixed allotment on first read each UTC day, can't be
//                  carried over. Bets debit this bucket first.
//   WinningsBalance — accumulates from payouts and persists indefinitely. Bets fall through
//                     to this only after DailyCredits is exhausted.
// DailyCreditsRefreshedOn is the UTC date the daily bucket was last topped up; null means
// the wallet has never been used (lazy refresh creates the row with today's allotment).
public sealed class UserWallet
{
    public long UserId { get; set; }
    public int DailyCredits { get; set; }
    public DateOnly? DailyCreditsRefreshedOn { get; set; }
    public long WinningsBalance { get; set; }
    public DateTimeOffset UpdatedAt { get; set; }

    public User? User { get; set; }
}
