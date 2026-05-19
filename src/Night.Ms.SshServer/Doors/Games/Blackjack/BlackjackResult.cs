namespace Night.Ms.SshServer.Doors.Games.Blackjack;

public enum BlackjackResult
{
    Loss,
    Push,
    Win,
    BlackjackWin,
}

public static class BlackjackResultExtensions
{
    public static string DisplayName(this BlackjackResult r) => r switch
    {
        BlackjackResult.Loss => "Lose",
        BlackjackResult.Push => "Push",
        BlackjackResult.Win => "Win",
        BlackjackResult.BlackjackWin => "Blackjack!",
        _ => r.ToString(),
    };
}
