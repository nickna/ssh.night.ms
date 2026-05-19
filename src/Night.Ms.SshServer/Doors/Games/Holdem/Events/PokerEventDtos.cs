namespace Night.Ms.SshServer.Doors.Games.Holdem.Events;

// Wire DTOs published by the coordinator. Wire encoding for a Card is its ToString() value
// — "As", "Td", etc. Hole cards in public events are nulled out for seats not at showdown.

public sealed record SeatSnapshotDto(
    int SeatIndex,
    string? Handle,
    string Kind,            // "user" | "cpu" | "empty"
    long Stack,
    long BetThisRound,
    long TotalContribution,
    string Status,
    string? Hole1,          // populated only in private snapshots or at showdown
    string? Hole2);

public sealed record HandStartedDto(
    int HandNumber,
    int DealerSeat,
    int SmallBlindSeat,
    int BigBlindSeat,
    long SmallBlind,
    long BigBlind,
    IReadOnlyList<SeatSnapshotDto> Seats);

public sealed record HoleCardsDealtDto(int HandNumber, int Seat, string Card1, string Card2);

public sealed record BoardDealtDto(
    int HandNumber,
    string Street,                          // "flop" | "turn" | "river"
    IReadOnlyList<string> NewCards,
    IReadOnlyList<string> BoardSoFar);

public sealed record ActionTakenDto(
    int HandNumber,
    int Seat,
    string Action,                          // "check" | "call" | "fold" | "bet" | "raise" | "all-in"
    long Amount,
    long Pot,
    long SeatStackAfter,
    long SeatBetAfter);

public sealed record StreetAdvancedDto(int HandNumber, string Street, long Pot);

public sealed record TurnStartedDto(
    int HandNumber,
    int Seat,
    DateTimeOffset DeadlineUtc,
    long MinRaise,
    long ToCall,
    IReadOnlyList<string> LegalActions);    // "check","call","fold","bet","raise","all-in"

public sealed record TimerTickDto(int HandNumber, int Seat, int RemainingSeconds);

public sealed record ShownHandDto(int Seat, string? Card1, string? Card2, bool Mucked);

public sealed record ShowdownStartedDto(int HandNumber, IReadOnlyList<ShownHandDto> ShownHands);

public sealed record HandPayoutDto(int Seat, long Amount, string Reason);

public sealed record HandEndedDto(
    int HandNumber,
    IReadOnlyList<HandPayoutDto> Payouts,
    IReadOnlyDictionary<int, long> FinalStacks);

public sealed record SeatChangedDto(
    int Seat,
    string Kind,                            // "sit-down" | "stand-up" | "sit-out" | "resume" | "timeout-sitout" | "cpu-seated"
    string? Handle,
    long Stack);

public sealed record SnapshotResyncDto(
    long ViewerUserId,
    int? HandNumber,
    string Phase,
    int DealerSeat,
    int? ActorSeat,
    DateTimeOffset? TurnDeadlineUtc,
    long CurrentBet,
    long MinRaise,
    long SmallBlind,
    long BigBlind,
    long Pot,
    IReadOnlyList<string> Board,
    IReadOnlyList<SeatSnapshotDto> Seats);

public sealed record ActionRejectedDto(int HandNumber, string Reason);
