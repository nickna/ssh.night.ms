using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui;
using Terminal.Gui.App;

namespace Night.Ms.SshServer.Doors;

// One door game (slots, video poker, future blackjack, etc.). Implementations register with
// DI as IDoorGame; DoorsScreen enumerates the lot and lists them. Key is the stable
// identifier written to game_rounds.game_key — used by leaderboards and audit. MinBet/MaxBet
// are surfaced to the user before they pick a game, so they know what they're walking into.
//
// CreateScreen returns a fresh BbsWindow each time the user opens the game. The screen owns
// the game loop and uses IWalletService + IGameLedger + IGameRng from the provided services
// scope. Returning a window (rather than a RunAsync task) matches the rest of the BBS — the
// lobby drives navigation via app.Run(window).
public interface IDoorGame
{
    string Key { get; }
    string Title { get; }
    string Description { get; }
    int MinBet { get; }
    int MaxBet { get; }

    BbsWindow CreateScreen(IApplication app, IServiceProvider services, User user);
}
