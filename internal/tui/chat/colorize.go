package chat

// HandleColor maps a handle to a stable hex color drawn from a curated palette
// tuned for legibility on the BBS dark background. Mirrors the .NET
// HandleColorizer: lowercased FNV-1a-32 over the bytes, modulo the palette
// size. The 16-entry palette is small on purpose — past ~16 colors the eye
// stops mapping color → identity and the readability win evaporates.
func HandleColor(handle string) string {
	if handle == "" {
		return ""
	}
	h := fnv1a(handle)
	return handlePalette[h%uint32(len(handlePalette))]
}

var handlePalette = [...]string{
	"#6CC0FF", // sky
	"#8EE09F", // mint
	"#FFB06B", // peach
	"#C8A2FF", // lavender
	"#FFC9DE", // rose
	"#9FE5D9", // seafoam
	"#E6C88A", // wheat
	"#B0E070", // lime
	"#FF9DBE", // pink
	"#A8E0C8", // pistachio
	"#7AC8FF", // azure
	"#C0D8A0", // sage
	"#E0B0D0", // mauve
	"#A0E0E0", // ice
	"#E0D090", // sand
	"#C0C0E8", // periwinkle
}

func fnv1a(s string) uint32 {
	const (
		offsetBasis uint32 = 2166136261
		fnvPrime    uint32 = 16777619
	)
	h := offsetBasis
	for i := 0; i < len(s); i++ {
		c := s[i]
		// Lowercase ASCII so "Alice" and "alice" share a color, matching .NET.
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		h ^= uint32(c)
		h *= fnvPrime
	}
	return h
}
