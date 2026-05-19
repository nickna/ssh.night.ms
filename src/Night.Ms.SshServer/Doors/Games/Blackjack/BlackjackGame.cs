using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui;
using Terminal.Gui.App;

namespace Night.Ms.SshServer.Doors.Games.Blackjack;

public sealed class BlackjackGame : IDoorGame
{
    public string Key => "blackjack";
    public string Title => "Blackjack";
    public string Description =>
        "Single-deck, dealer stands on soft 17, blackjack pays 3:2. Hit / Stand / Double / Split.";

    // Bet steps of 10 keep 3:2 blackjack payouts integer at every legal bet.
    public int MinBet => 10;
    public int MaxBet => 100;

    public BbsWindow CreateScreen(IApplication app, IServiceProvider services, User user) =>
        new BlackjackScreen(app, services, user, this);
}
