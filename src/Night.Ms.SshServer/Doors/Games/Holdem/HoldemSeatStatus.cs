namespace Night.Ms.SshServer.Doors.Games.Holdem;

public enum HoldemSeatStatus
{
    Empty,             // no one is occupying this seat
    Active,            // playing the current hand and able to act
    Folded,            // out of the current hand; chip contribution stays in the pot
    AllIn,             // committed full stack; can't act further but eligible for showdown
    SittingOut,        // seated but skipped for this hand (disconnect / 3-miss / explicit)
    AwaitingNextHand,  // joined mid-hand or just paid blinds-in; deals into the next hand
}
