namespace Night.Ms.SshServer.Doors.Games.Holdem;

// Pure layout math for the spatial table view, extracted so tests can exercise it without
// touching Terminal.Gui. Loading HoldemTableView from xUnit triggers TG's ConfigurationManager
// module initializer (which crashes outside an interactive app context) — keeping this file
// free of TG references means HoldemTableViewTests can reference these types directly.
internal enum HoldemSeatPosition
{
    TopLeft, TopCenter, TopRight,
    BottomLeft, Viewer, BottomRight,
}

internal static class HoldemSeatLayout
{
    // Layout position for a given seat index given the viewer's seat (or null for
    // spectator). Viewer is pinned to bottom-center; seats fan clockwise from there.
    //   offset 0 → Viewer        offset 3 → TopCenter
    //   offset 1 → BottomRight   offset 4 → TopLeft
    //   offset 2 → TopRight      offset 5 → BottomLeft
    public static HoldemSeatPosition MapSeatToPosition(int seatIndex, int? viewerSeat, int seatCount = 6)
    {
        var pivot = viewerSeat ?? 0;
        // C#'s % can return negative — normalize to [0, seatCount).
        var offset = ((seatIndex - pivot) % seatCount + seatCount) % seatCount;
        return offset switch
        {
            0 => HoldemSeatPosition.Viewer,
            1 => HoldemSeatPosition.BottomRight,
            2 => HoldemSeatPosition.TopRight,
            3 => HoldemSeatPosition.TopCenter,
            4 => HoldemSeatPosition.TopLeft,
            5 => HoldemSeatPosition.BottomLeft,
            _ => HoldemSeatPosition.Viewer,
        };
    }
}
