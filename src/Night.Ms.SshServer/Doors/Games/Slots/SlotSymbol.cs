namespace Night.Ms.SshServer.Doors.Games.Slots;

// Six paying symbols plus Blank. Order matters: it's the array index used in the reel
// distribution and the paytable lookup, and the persisted "details" jsonb on game_rounds
// stores symbol names by ToString() — renaming a value is a schema-compat break.
public enum SlotSymbol
{
    Blank,
    Cherry,
    Lemon,
    Plum,
    Bell,
    Bar,
    Seven,
}

public static class SlotSymbolExtensions
{
    // Display glyph for the reel window. ASCII-only on purpose: emoji or box-drawing symbols
    // render at unpredictable widths in some SSH clients, which would misalign the reel row.
    public static char Glyph(this SlotSymbol s) => s switch
    {
        SlotSymbol.Seven => '7',
        SlotSymbol.Bar => 'B',
        SlotSymbol.Bell => 'b',
        SlotSymbol.Plum => 'P',
        SlotSymbol.Lemon => 'L',
        SlotSymbol.Cherry => 'C',
        SlotSymbol.Blank => '-',
        _ => '?',
    };

    public static string DisplayName(this SlotSymbol s) => s switch
    {
        SlotSymbol.Seven => "Seven",
        SlotSymbol.Bar => "Bar",
        SlotSymbol.Bell => "Bell",
        SlotSymbol.Plum => "Plum",
        SlotSymbol.Lemon => "Lemon",
        SlotSymbol.Cherry => "Cherry",
        SlotSymbol.Blank => "Blank",
        _ => "?",
    };
}
