using Terminal.Gui.Input;

namespace Night.Ms.SshServer.Tui;

internal static class KeyExtensions
{
    // Terminal.Gui v2 treats `R` and `Shift+R` as distinct keys, so every "case-insensitive"
    // hotkey check needs to test both. Without this helper a caps-lock-on user gets a dead
    // hotkey — easy to forget when adding a new binding.
    public static bool Matches(this Key key, Key letter) =>
        key == letter || key == letter.WithShift;
}
