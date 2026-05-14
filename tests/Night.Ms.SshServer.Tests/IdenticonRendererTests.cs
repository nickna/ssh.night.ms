using Night.Ms.SshServer.Web;
using SixLabors.ImageSharp;
using SixLabors.ImageSharp.PixelFormats;

namespace Night.Ms.SshServer.Tests;

public class IdenticonRendererTests
{
    [Fact]
    public void Same_handle_yields_pixel_identical_image_across_calls()
    {
        using var a = IdenticonRenderer.Generate("alice");
        using var b = IdenticonRenderer.Generate("alice");
        Assert.True(ImagesEqual(a, b));
    }

    [Fact]
    public void Case_insensitive_lookup_yields_same_image()
    {
        using var lower = IdenticonRenderer.Generate("alice");
        using var mixed = IdenticonRenderer.Generate("Alice");
        Assert.True(ImagesEqual(lower, mixed));
    }

    [Fact]
    public void Different_handles_yield_different_images()
    {
        using var a = IdenticonRenderer.Generate("alice");
        using var b = IdenticonRenderer.Generate("bob");
        Assert.False(ImagesEqual(a, b));
    }

    [Fact]
    public void Default_size_is_256()
    {
        using var img = IdenticonRenderer.Generate("alice");
        Assert.Equal(256, img.Width);
        Assert.Equal(256, img.Height);
    }

    [Fact]
    public void Custom_size_is_respected()
    {
        using var img = IdenticonRenderer.Generate("alice", size: 64);
        Assert.Equal(64, img.Width);
        Assert.Equal(64, img.Height);
    }

    [Fact]
    public void Pattern_is_left_right_symmetric()
    {
        using var img = IdenticonRenderer.Generate("alice");
        // The figure spans tiles[origin..origin+5*tilePx] in both axes; we sample the center
        // of each tile to check symmetry without coupling to the exact tile size.
        const int tiles = 5;
        var tilePx = img.Width / 7; // matches the 5+2-margin layout in IdenticonRenderer
        var origin = (img.Width - tilePx * tiles) / 2;
        for (var y = 0; y < tiles; y++)
        {
            for (var x = 0; x < tiles / 2; x++)
            {
                var leftCenter = (origin + x * tilePx + tilePx / 2, origin + y * tilePx + tilePx / 2);
                var rightCenter = (origin + (tiles - 1 - x) * tilePx + tilePx / 2, origin + y * tilePx + tilePx / 2);
                Assert.Equal(img[leftCenter.Item1, leftCenter.Item2], img[rightCenter.Item1, rightCenter.Item2]);
            }
        }
    }

    private static bool ImagesEqual(Image<Rgba32> a, Image<Rgba32> b)
    {
        if (a.Width != b.Width || a.Height != b.Height) return false;
        for (var y = 0; y < a.Height; y++)
        {
            for (var x = 0; x < a.Width; x++)
            {
                if (!a[x, y].Equals(b[x, y])) return false;
            }
        }
        return true;
    }
}
