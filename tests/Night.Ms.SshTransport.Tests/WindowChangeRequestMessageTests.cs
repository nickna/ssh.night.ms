using Microsoft.DevTunnels.Ssh.IO;
using Microsoft.DevTunnels.Ssh.Messages;

namespace Night.Ms.SshTransport.Tests;

public class WindowChangeRequestMessageTests
{
    [Fact]
    public void Round_trip_preserves_dimensions_and_request_type()
    {
        var original = new WindowChangeRequestMessage
        {
            RequestType = WindowChangeRequestMessage.RequestTypeName,
            WantReply = false,
            RecipientChannel = 1,
            Columns = 132,
            Rows = 50,
            PixelWidth = 1056,
            PixelHeight = 800,
        };

        var bytes = original.ToBuffer();

        // Replay through the base class first (the way ChannelRequestMessage arrives over the
        // wire), then convert — same shape DevTunnels uses for typed signal messages.
        var baseMsg = new ChannelRequestMessage();
        var reader = new SshDataReader(bytes);
        // Skip the leading message-type byte (98 for SSH_MSG_CHANNEL_REQUEST).
        reader.ReadByte();
        baseMsg.GetType()
            .GetMethod("OnRead", System.Reflection.BindingFlags.NonPublic | System.Reflection.BindingFlags.Instance)!
            .Invoke(baseMsg, new object[] { reader });

        Assert.Equal(WindowChangeRequestMessage.RequestTypeName, baseMsg.RequestType);

        // Re-read the full message into the typed subclass via the same path the channel uses.
        var typed = new WindowChangeRequestMessage();
        var reader2 = new SshDataReader(bytes);
        reader2.ReadByte();
        typed.GetType()
            .GetMethod("OnRead", System.Reflection.BindingFlags.NonPublic | System.Reflection.BindingFlags.Instance)!
            .Invoke(typed, new object[] { reader2 });

        Assert.Equal(132u, typed.Columns);
        Assert.Equal(50u, typed.Rows);
        Assert.Equal(1056u, typed.PixelWidth);
        Assert.Equal(800u, typed.PixelHeight);
    }
}
