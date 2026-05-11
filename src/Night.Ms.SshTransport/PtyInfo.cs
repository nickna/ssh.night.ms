namespace Night.Ms.SshTransport;

public sealed record PtyInfo(string Terminal, uint Cols, uint Rows, uint PixelWidth, uint PixelHeight);

public sealed record WindowChange(uint Cols, uint Rows, uint PixelWidth, uint PixelHeight);
