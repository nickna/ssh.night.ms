using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.Drawing;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Views;

// One-line status row used inside screens for transient feedback ("Saved.", "[!] foo failed").
// Owns the [!]-prefix → Warning heuristic so callers don't reimplement it. Set the DefaultKind
// initializer when the resting state should be Status (dim cyan) instead of Hint (bright cyan).
internal sealed class BbsStatusLine : Label
{
    public enum StatusKind
    {
        Hint,
        Status,
        Warning,
        Success,
    }

    public StatusKind DefaultKind { get; init; } = StatusKind.Hint;

    public BbsStatusLine()
    {
        SetScheme(BbsTheme.Hint);
    }

    public void Set(string text) => Set(text, DefaultKind);

    public void Set(string text, StatusKind kind)
    {
        Text = text;
        SetScheme(SchemeFor(text, kind));
    }

    public void SetWarning(string text) => Set(text, StatusKind.Warning);

    public void SetSuccess(string text) => Set(text, StatusKind.Success);

    private static Scheme SchemeFor(string text, StatusKind kind)
    {
        if (kind == StatusKind.Warning || text.StartsWith("[!]", StringComparison.Ordinal))
        {
            return BbsTheme.Warning;
        }
        return kind switch
        {
            StatusKind.Success => BbsTheme.Success_,
            StatusKind.Status => BbsTheme.Status,
            _ => BbsTheme.Hint,
        };
    }
}
