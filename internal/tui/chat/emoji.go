package chat

import "strings"

// SubstituteEmoji rewrites :shortcode: tokens to their unicode glyphs. Unknown
// shortcodes pass through untouched so a typo doesn't eat the rest of the
// message. Mirrors the .NET EmojiTable.Substitute behavior including the 32-
// char lookahead cap so pathological inputs stay cheap.
func SubstituteEmoji(text string) string {
	if text == "" || !strings.Contains(text, ":") {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	i := 0
	for i < len(text) {
		if text[i] != ':' {
			b.WriteByte(text[i])
			i++
			continue
		}
		// Look ahead for a closing colon, bailing on the first non-shortcode
		// byte. Cap at 32 chars — every entry in the table is shorter than
		// that and the cap stops a stray ":" from scanning to EOL.
		end := i + 32
		if end > len(text) {
			end = len(text)
		}
		close := -1
		for j := i + 1; j < end; j++ {
			c := text[j]
			if c == ':' {
				close = j
				break
			}
			if !isShortcodeByte(c) {
				break
			}
		}
		if close > i+1 {
			code := strings.ToLower(text[i+1 : close])
			if glyph, ok := emojiTable[code]; ok {
				b.WriteString(glyph)
				i = close + 1
				continue
			}
		}
		b.WriteByte(':')
		i++
	}
	return b.String()
}

func isShortcodeByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '_' || c == '+' || c == '-':
		return true
	}
	return false
}

// emojiTable is the same curated set the .NET build ships. Keep entries
// terminal-friendly: every glyph must render as a single grapheme in the
// monospace fonts BBS users typically have (Cascadia Mono, JetBrains Mono,
// Iosevka, Menlo, Source Code Pro).
var emojiTable = map[string]string{
	// Faces / expressions
	"smile":          "\U0001F600",
	"grin":           "\U0001F601",
	"joy":            "\U0001F602",
	"laughing":       "\U0001F606",
	"wink":           "\U0001F609",
	"blush":          "\U0001F60A",
	"sunglasses":     "\U0001F60E",
	"thinking":       "\U0001F914",
	"neutral":        "\U0001F610",
	"expressionless": "\U0001F611",
	"unamused":       "\U0001F612",
	"sweat":          "\U0001F613",
	"pensive":        "\U0001F614",
	"confused":       "\U0001F615",
	"worried":        "\U0001F61F",
	"cry":            "\U0001F622",
	"sob":            "\U0001F62D",
	"scream":         "\U0001F631",
	"angry":          "\U0001F620",
	"rage":           "\U0001F621",
	"heart_eyes":     "\U0001F60D",
	"kiss":           "\U0001F618",
	"zipper_mouth":   "\U0001F910",
	"sleeping":       "\U0001F634",
	"dizzy_face":     "\U0001F635",
	"nerd":           "\U0001F913",
	"robot":          "\U0001F916",
	"clown":          "\U0001F921",
	"facepalm":       "\U0001F926",
	"shrug":          "\U0001F937",
	"wave":           "\U0001F44B",

	// Hands / gestures
	"thumbsup":     "\U0001F44D",
	"+1":           "\U0001F44D",
	"thumbsdown":   "\U0001F44E",
	"-1":           "\U0001F44E",
	"clap":         "\U0001F44F",
	"raised_hands": "\U0001F64C",
	"pray":         "\U0001F64F",
	"ok_hand":      "\U0001F44C",
	"point_up":     "☝️",
	"muscle":       "\U0001F4AA",
	"fist":         "✊",
	"v":            "✌️",

	// Symbols / hearts
	"heart":        "❤️",
	"broken_heart": "\U0001F494",
	"sparkles":     "✨",
	"star":         "⭐",
	"fire":         "\U0001F525",
	"100":          "\U0001F4AF",
	"check":        "✅",
	"x":            "❌",
	"warning":      "⚠️",
	"question":     "❓",
	"exclamation":  "❗",
	"zap":          "⚡",
	"boom":         "\U0001F4A5",
	"sparkle":      "❇️",
	"eyes":         "\U0001F440",
	"bulb":         "\U0001F4A1",
	"tada":         "\U0001F389",
	"rocket":       "\U0001F680",
	"bug":          "\U0001F41B",
	"beer":         "\U0001F37A",
	"coffee":       "☕",
	"pizza":        "\U0001F355",
	"cake":         "\U0001F370",
	"cat":          "\U0001F408",
	"dog":          "\U0001F415",
	"robot_face":   "\U0001F916",
	"computer":     "\U0001F4BB",
	"phone":        "\U0001F4F1",
	"mailbox":      "\U0001F4EE",
	"lock":         "\U0001F512",
	"key":          "\U0001F511",
	"moon":         "\U0001F319",
	"sun":          "☀️",
	"cloud":        "☁️",
	"snowflake":    "❄️",
	"umbrella":     "☔",
	"tree":         "\U0001F333",
	"earth":        "\U0001F30D",

	// Plumbing
	"shipit":       "\U0001F69A",
	"ship":         "\U0001F6A2",
	"hammer":       "\U0001F528",
	"wrench":       "\U0001F527",
	"package":      "\U0001F4E6",
	"construction": "\U0001F6A7",
	"lgtm":         "\U0001F44D",
}
