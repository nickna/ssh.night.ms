using Night.Ms.SshServer.Doors.Games.Holdem;

namespace Night.Ms.SshServer.Tests.Doors.Games.Holdem;

// Tests for the spatial-table seat rotation. The math lives in HoldemSeatLayout (not
// HoldemTableView) so xUnit doesn't have to load Terminal.Gui to resolve method signatures —
// TG's ConfigurationManager module initializer crashes in the test process.
//
// Parameters use int (cast from the internal HoldemSeatPosition enum) because xUnit only
// discovers public test classes, and a public method can't expose an internal enum.
public class HoldemTableViewTests
{
    private const int TopLeft = (int)HoldemSeatPosition.TopLeft;
    private const int TopCenter = (int)HoldemSeatPosition.TopCenter;
    private const int TopRight = (int)HoldemSeatPosition.TopRight;
    private const int BottomLeft = (int)HoldemSeatPosition.BottomLeft;
    private const int Viewer = (int)HoldemSeatPosition.Viewer;
    private const int BottomRight = (int)HoldemSeatPosition.BottomRight;

    [Theory]
    // viewer seat 0: rotation lines up with seat index 1:1.
    [InlineData(0, 0, Viewer)]
    [InlineData(1, 0, BottomRight)]
    [InlineData(2, 0, TopRight)]
    [InlineData(3, 0, TopCenter)]
    [InlineData(4, 0, TopLeft)]
    [InlineData(5, 0, BottomLeft)]
    // viewer seat 3: shifts everything by 3.
    [InlineData(3, 3, Viewer)]
    [InlineData(4, 3, BottomRight)]
    [InlineData(5, 3, TopRight)]
    [InlineData(0, 3, TopCenter)]
    [InlineData(1, 3, TopLeft)]
    [InlineData(2, 3, BottomLeft)]
    // viewer seat 5: wraps cleanly.
    [InlineData(5, 5, Viewer)]
    [InlineData(0, 5, BottomRight)]
    [InlineData(1, 5, TopRight)]
    public void MapSeatToPosition_RotatesViewerToBottomCenter(int seatIndex, int viewerSeat, int expectedPosition)
    {
        var actual = HoldemSeatLayout.MapSeatToPosition(seatIndex, viewerSeat);
        Assert.Equal((HoldemSeatPosition)expectedPosition, actual);
    }

    [Fact]
    public void MapSeatToPosition_SpectatorUsesSeatZeroAsPivot()
    {
        // Null viewer (spectator) → fixed orientation pivoted on seat 0, so seat 0 lands
        // at Viewer and the rest fan out clockwise. Keeps the layout stable when other
        // players come and go.
        Assert.Equal(HoldemSeatPosition.Viewer, HoldemSeatLayout.MapSeatToPosition(0, null));
        Assert.Equal(HoldemSeatPosition.BottomRight, HoldemSeatLayout.MapSeatToPosition(1, null));
        Assert.Equal(HoldemSeatPosition.TopRight, HoldemSeatLayout.MapSeatToPosition(2, null));
        Assert.Equal(HoldemSeatPosition.TopCenter, HoldemSeatLayout.MapSeatToPosition(3, null));
        Assert.Equal(HoldemSeatPosition.TopLeft, HoldemSeatLayout.MapSeatToPosition(4, null));
        Assert.Equal(HoldemSeatPosition.BottomLeft, HoldemSeatLayout.MapSeatToPosition(5, null));
    }

    [Fact]
    public void MapSeatToPosition_HandlesNegativeRotationCorrectly()
    {
        // Modular arithmetic must produce a positive offset even when (seatIndex - pivot)
        // is negative — C#'s % operator returns negative for negative dividends.
        Assert.Equal(HoldemSeatPosition.TopLeft, HoldemSeatLayout.MapSeatToPosition(1, 3));
        Assert.Equal(HoldemSeatPosition.BottomLeft, HoldemSeatLayout.MapSeatToPosition(2, 3));
    }
}
