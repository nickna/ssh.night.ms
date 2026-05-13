using Terminal.Gui.Drawing;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;

namespace Night.Ms.SshServer.Tui.Theme;

// Modern-retro BBS palette + a handful of named Schemes. Screens call ApplyWindow(this) at
// the top of their constructor so the window background and default text colors are set;
// they reach for the named accent Schemes below on specific labels/status lines for
// contrast (headers in bright yellow, status lines in dim cyan, error lines in red, etc.).
//
// The palette intentionally sticks to the 16 ANSI colors — that keeps SGR output short and
// renders the same on PuTTY, Windows Terminal, iTerm2, kitty, and mosh. No 256/truecolor
// codes.
internal static class BbsTheme
{
    // Palette — five accents on a black background, mirroring 90s BBS art. Kept internal so
    // article/chat palettes can compose the same anchor colors instead of redefining them.
    internal static readonly Color Bg = Color.Black;
    internal static readonly Color Body = Color.Gray;             // primary body text
    internal static readonly Color BodyBright = Color.White;      // emphasized body text
    internal static readonly Color Accent = Color.BrightCyan;     // chrome accents, button glyphs
    internal static readonly Color AccentDim = Color.Cyan;        // status text
    internal static readonly Color Header = Color.BrightYellow;   // headers / hotkeys
    internal static readonly Color HighlightBg = Color.BrightMagenta; // selection bar
    internal static readonly Color Success = Color.BrightGreen;
    internal static readonly Color Warn = Color.BrightRed;
    internal static readonly Color Faint = Color.DarkGray;
    internal static readonly Color InputBg = Color.Blue;

    // Default scheme inherited by all controls inside a window: gray body on black, with
    // hotkeys bright-yellow + bold. Buttons + lists derive their focus look from this.
    public static readonly Scheme Window = new()
    {
        Normal     = new Attribute(Body, Bg),
        HotNormal  = new Attribute(Header, Bg, TextStyle.Bold),
        Focus      = new Attribute(Bg, Accent, TextStyle.Bold),
        HotFocus   = new Attribute(Bg, Header, TextStyle.Bold),
        Active     = new Attribute(BodyBright, Bg, TextStyle.Bold),
        HotActive  = new Attribute(Header, Bg, TextStyle.Bold),
        Highlight  = new Attribute(BodyBright, HighlightBg, TextStyle.Bold),
        Disabled   = new Attribute(Faint, Bg),
    };

    // Bright-yellow bold — for top-of-screen titles and section headers.
    public static readonly Scheme Header_ = SingleRole(new Attribute(Header, Bg, TextStyle.Bold));

    // Dim cyan — for the bottom status bar, "updated HH:mm:ss" timestamps, etc.
    public static readonly Scheme Status = SingleRole(new Attribute(AccentDim, Bg));

    // Bright cyan — for hint/help text and key bindings rendered in-line.
    public static readonly Scheme Hint = new()
    {
        Normal = new Attribute(Accent, Bg),
        HotNormal = new Attribute(Header, Bg, TextStyle.Bold),
    };

    // Faded gray — for fingerprints, deemphasized metadata, ASCII art.
    public static readonly Scheme Faint_ = SingleRole(new Attribute(Faint, Bg));

    // Bright green bold — for welcome-aboard / "saved" success messages.
    public static readonly Scheme Success_ = SingleRole(new Attribute(Success, Bg, TextStyle.Bold));

    // Bright red bold — for "[!] failed" / error toasts.
    public static readonly Scheme Warning = SingleRole(new Attribute(Warn, Bg, TextStyle.Bold));

    // White on blue — input fields stand out from the gray body text. Buttons are still
    // black-on-cyan via the Window scheme; this is just for TextField / TextView.
    public static readonly Scheme Input = new()
    {
        Normal     = new Attribute(BodyBright, InputBg),
        Focus      = new Attribute(BodyBright, InputBg, TextStyle.Bold),
        Editable   = new Attribute(BodyBright, InputBg),
        ReadOnly   = new Attribute(Body, Bg),
    };

    public static void ApplyWindow(View view) => view.SetScheme(Window);

    private static Scheme SingleRole(Attribute attribute) => new(attribute);
}
