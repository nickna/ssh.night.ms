using System.Text;

namespace Night.Ms.SshServer.Tui.Art;

// One terminal cell: glyph + foreground + background + style. Colors are stored as 24-bit
// RGB to keep this assembly testable without loading Terminal.Gui (see ArtColor).
//
// Modifier is an optional second Unicode scalar emitted immediately after Glyph in the same
// cell — used for variation selectors like U+FE0E (text presentation) on the playing-card
// suit pips ♠♥♦♣, which some clients otherwise render double-wide as emoji and break row
// alignment. Renderers must emit "{Glyph}{Modifier}" via AddStr (not two AddRunes) so the
// terminal treats the modifier as a zero-width attachment to the base glyph rather than as
// its own cell.
internal readonly record struct Cell(Rune Glyph, ArtColor Foreground, ArtColor Background, ArtStyle Style, Rune? Modifier = null)
{
    public static readonly Cell Empty = new(new Rune(' '), ArtColor.DefaultForeground, ArtColor.DefaultBackground, ArtStyle.None);
}
