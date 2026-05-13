using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Tui.Chat;

// Maps a handle to a stable, readable color. We pick from a curated 16-entry palette tuned
// for legibility on the BBS black background: no near-blacks, no near-yellows (collide with
// our own header color), no near-reds (reserved for errors). FNV-1a-32 over the lowercased
// handle bytes gives us a fast, allocation-free index that's stable across processes.
//
// 16 entries is deliberately small — with more colors the eye stops mapping color → identity
// (you just see "some warm-ish name") and the readability win evaporates. 16 collides at
// ~5% pairwise (birthday paradox at handle count ≈ 5) which is fine for a BBS.
internal static class HandleColorizer
{
    private static readonly ArtColor[] Palette =
    [
        new(0x6C, 0xC0, 0xFF), // sky
        new(0x8E, 0xE0, 0x9F), // mint
        new(0xFF, 0xB0, 0x6B), // peach
        new(0xC8, 0xA2, 0xFF), // lavender
        new(0xFF, 0xC9, 0xDE), // rose
        new(0x9F, 0xE5, 0xD9), // seafoam
        new(0xE6, 0xC8, 0x8A), // wheat
        new(0xB0, 0xE0, 0x70), // lime
        new(0xFF, 0x9D, 0xBE), // pink
        new(0xA8, 0xE0, 0xC8), // pistachio
        new(0x7A, 0xC8, 0xFF), // azure
        new(0xC0, 0xD8, 0xA0), // sage
        new(0xE0, 0xB0, 0xD0), // mauve
        new(0xA0, 0xE0, 0xE0), // ice
        new(0xE0, 0xD0, 0x90), // sand
        new(0xC0, 0xC0, 0xE8), // periwinkle
    ];

    public static ArtColor ColorFor(string handle)
    {
        if (string.IsNullOrEmpty(handle)) return ArtColor.DefaultForeground;
        var hash = Fnv1a(handle);
        return Palette[hash % (uint)Palette.Length];
    }

    private static uint Fnv1a(string s)
    {
        const uint offsetBasis = 2166136261u;
        const uint fnvPrime = 16777619u;
        var hash = offsetBasis;
        foreach (var ch in s)
        {
            // Hash on the lowercased char so "Alice" and "alice" share a color.
            var c = char.IsAsciiLetterUpper(ch) ? (char)(ch + 32) : ch;
            hash ^= c;
            hash *= fnvPrime;
        }
        return hash;
    }
}
