namespace Night.Ms.SshServer.Tui.Art;

// Mirrors the subset of Terminal.Gui's TextStyle we actually carry through the art pipeline
// and the chat renderer. Kept separate so the art/chat types don't have to load Terminal.Gui
// in test contexts.
[Flags]
internal enum ArtStyle
{
    None = 0,
    Bold = 1 << 0,
    Italic = 1 << 1,
    Underline = 1 << 2,
}
