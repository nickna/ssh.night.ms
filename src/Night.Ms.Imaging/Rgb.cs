namespace Night.Ms.Imaging;

// 24-bit RGB triplet used by the half-block renderer and palette quantization. Kept
// distinct from System.Drawing.Color / ImageSharp's Rgb24 so this assembly's public API
// doesn't drag a specific image library into its callers.
public readonly record struct Rgb(byte R, byte G, byte B);
