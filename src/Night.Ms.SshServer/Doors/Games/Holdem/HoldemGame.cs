using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Doors.Multiplayer;
using Night.Ms.SshServer.Tui;
using Terminal.Gui.App;

namespace Night.Ms.SshServer.Doors.Games.Holdem;

// IMultiplayerDoor registration. Discovered via services.GetServices<IDoorGame>() the same
// way slots/blackjack/videopoker are; DoorsScreen renders it next to them. MinBet/MaxBet
// surface as the buy-in chip range in the carousel description.
public sealed class HoldemGame : IMultiplayerDoor
{
    public string Key => "holdem";
    public string Title => "Hold'em (6-max)";
    public string Description =>
        "No-limit Texas Hold'em. 5/10 blinds, six seats, watch or play. CPUs keep the table warm.";
    public int MinBet => HoldemRules.DefaultMinBuyInChips;
    public int MaxBet => HoldemRules.DefaultMaxBuyInChips;

    public int MaxConcurrentTables => 1;
    public int SeatsPerTable => HoldemRules.MaxSeats;
    public int MinSeatedToStart => HoldemRules.MinSeatedToStart;
    public long SmallBlind => HoldemRules.DefaultSmallBlind;
    public long BigBlind => HoldemRules.DefaultBigBlind;

    public BbsWindow CreateScreen(IApplication app, IServiceProvider services, User user) =>
        new HoldemScreen(app, services, user);
}
