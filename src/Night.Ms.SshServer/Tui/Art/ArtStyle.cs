namespace Night.Ms.SshServer.Tui.Art;

// Mirrors the subset of Terminal.Gui's TextStyle we actually carry through the art pipeline.
// Kept separate so the art types don't have to load Terminal.Gui in test contexts.
[Flags]
internal enum ArtStyle
{
    None = 0,
    Bold = 1 << 0,
}
