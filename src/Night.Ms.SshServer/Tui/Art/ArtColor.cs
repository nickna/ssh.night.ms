namespace Night.Ms.SshServer.Tui.Art;

// 24-bit RGB color used by the art pipeline. We deliberately don't use Terminal.Gui's Color
// type here so that the parser + grid types are testable without loading Terminal.Gui (its
// ModuleInitializer scans every loaded assembly and falls over inside the xUnit process).
// AnsiArtView wraps these into Terminal.Gui Colors at paint time.
internal readonly record struct ArtColor(byte R, byte G, byte B)
{
    public static readonly ArtColor Black = new(0, 0, 0);
    public static readonly ArtColor DefaultForeground = new(170, 170, 170);
    public static readonly ArtColor DefaultBackground = Black;
}
