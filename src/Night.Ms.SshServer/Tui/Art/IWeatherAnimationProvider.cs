using Night.Ms.SshServer.Providers;

namespace Night.Ms.SshServer.Tui.Art;

// Resolves the animated banner that WeatherScreen renders for a given weather condition.
// Implementations decide where the frames come from (filesystem in v1; possibly a DB-backed
// asset store later) — the screen only cares about "give me an ordered list of CellGrids
// for this condition".
//
// Frames are returned in display order (typically the natural sort of frame-NN filenames).
// An empty list means "no animation for this condition" — the screen hides the banner area
// gracefully rather than rendering a blank rectangle.
internal interface IWeatherAnimationProvider
{
    // Returns the frames for the requested condition, falling back to the "default" scene
    // when the condition has no frames of its own. Cached in memory so a 500ms frame pump
    // doesn't re-read the filesystem on every tick.
    IReadOnlyList<CellGrid> GetFrames(WeatherCondition condition);
}
