using System.Text;

namespace Night.Ms.SshServer.Tui.Art;

// One terminal cell: glyph + foreground + background + style. Colors are stored as 24-bit
// RGB to keep this assembly testable without loading Terminal.Gui (see ArtColor).
internal readonly record struct Cell(Rune Glyph, ArtColor Foreground, ArtColor Background, ArtStyle Style)
{
    public static readonly Cell Empty = new(new Rune(' '), ArtColor.DefaultForeground, ArtColor.DefaultBackground, ArtStyle.None);
}
