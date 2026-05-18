using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui;
using Terminal.Gui.App;

namespace Night.Ms.SshServer.Doors.Games.Slots;

public sealed class SlotsGame : IDoorGame
{
    public string Key => "slots";
    public string Title => "Slot Machine";
    public string Description =>
        "Three reels of weighted symbols. Match three for a multiplier; two cherries or one cherry on reel 1 pays too.";
    public int MinBet => 5;
    public int MaxBet => 50;

    public BbsWindow CreateScreen(IApplication app, IServiceProvider services, User user) =>
        new SlotsScreen(app, services, user, this);
}
