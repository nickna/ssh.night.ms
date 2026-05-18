using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui;
using Terminal.Gui.App;

namespace Night.Ms.SshServer.Doors.Games.VideoPoker;

public sealed class VideoPokerGame : IDoorGame
{
    public string Key => "videopoker";
    public string Title => "Video Poker";
    public string Description =>
        "Five-card draw, 9/6 Jacks-or-Better. Bet 5 (1 coin) to 25 (max coin). Max-coin Royal Flush jumps to 4000.";

    // Bet steps are 5/10/15/20/25 — exactly the "1 coin / 2 coin / .. / max coin" axis used
    // by VideoPokerPaytable. Adjusting in 5s naturally lands on a valid coin level.
    public int MinBet => 5;
    public int MaxBet => 25;

    public BbsWindow CreateScreen(IApplication app, IServiceProvider services, User user) =>
        new VideoPokerScreen(app, services, user, this);
}
