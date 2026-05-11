using Microsoft.DevTunnels.Ssh.IO;
using Microsoft.DevTunnels.Ssh.Messages;

namespace Night.Ms.SshTransport;

// SSH window-change channel request payload (RFC 4254 §6.7): four uint32 values that
// follow the standard request-type + want-reply prefix. DevTunnels delivers it as a
// base ChannelRequestMessage; we convert via SshMessage.ConvertTo<T> to read the rest.
public sealed class WindowChangeRequestMessage : ChannelRequestMessage
{
    public const string RequestTypeName = "window-change";

    public uint Columns { get; set; }
    public uint Rows { get; set; }
    public uint PixelWidth { get; set; }
    public uint PixelHeight { get; set; }

    protected override void OnRead(ref SshDataReader reader)
    {
        base.OnRead(ref reader);
        Columns = reader.ReadUInt32();
        Rows = reader.ReadUInt32();
        PixelWidth = reader.ReadUInt32();
        PixelHeight = reader.ReadUInt32();
    }

    protected override void OnWrite(ref SshDataWriter writer)
    {
        base.OnWrite(ref writer);
        writer.Write(Columns);
        writer.Write(Rows);
        writer.Write(PixelWidth);
        writer.Write(PixelHeight);
    }
}
