namespace Night.Ms.SshServer.Doors;

// Point-in-time view of a user's coin balance. DailyCredits resets each UTC day to
// DailyAllotment; WinningsBalance carries forward indefinitely. Total is what's
// actually spendable on a bet.
public sealed record WalletSnapshot(int DailyCredits, long WinningsBalance, int DailyAllotment)
{
    public long Total => DailyCredits + WinningsBalance;
}
