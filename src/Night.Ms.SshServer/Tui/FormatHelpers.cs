using System.Globalization;

namespace Night.Ms.SshServer.Tui;

internal static class FormatHelpers
{
    public static string HumanizeAge(TimeSpan age)
    {
        if (age.TotalMinutes < 60) return $"{(int)Math.Max(1, age.TotalMinutes)}m ago";
        if (age.TotalHours < 24) return $"{(int)age.TotalHours}h ago";
        return $"{(int)age.TotalDays}d ago";
    }

    public static string Truncate(string s, int max) => s.Length <= max ? s : s[..max];

    public static string CurrencyGlyph(string currency) => currency switch
    {
        "USD" => "$",
        "EUR" => "€",
        "GBP" => "£",
        "JPY" => "¥",
        _ => string.Empty,
    };
}
