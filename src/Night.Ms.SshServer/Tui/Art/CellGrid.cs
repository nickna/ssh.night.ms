namespace Night.Ms.SshServer.Tui.Art;

// Rectangular grid of Cells. Width is the widest row; shorter rows are right-padded with
// Cell.Empty on construction so callers can index in bounds without bookkeeping.
internal sealed class CellGrid
{
    private readonly Cell[,] _cells;

    public int Width { get; }
    public int Height { get; }

    public CellGrid(int width, int height)
    {
        if (width < 0) throw new ArgumentOutOfRangeException(nameof(width));
        if (height < 0) throw new ArgumentOutOfRangeException(nameof(height));
        Width = width;
        Height = height;
        _cells = new Cell[height, width];
        for (var y = 0; y < height; y++)
            for (var x = 0; x < width; x++)
                _cells[y, x] = Cell.Empty;
    }

    public Cell this[int x, int y]
    {
        get => _cells[y, x];
        set => _cells[y, x] = value;
    }
}
