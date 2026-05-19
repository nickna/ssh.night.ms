using System.Text.Json;

namespace Night.Ms.SshServer.Doors.Games.Holdem.Events;

// Parses the PokerEventEnvelope wire form and routes to typed handlers. Mirrors the
// ChatEnvelopeDispatcher pattern. Handlers run on the subscriber background task; they
// must marshal view mutations to the UI thread via IApplication.Invoke.
public sealed class HoldemEventDispatcher
{
    public Action<HandStartedDto>? OnHandStarted { get; set; }
    public Action<HoleCardsDealtDto>? OnHoleCardsDealt { get; set; }
    public Action<BoardDealtDto>? OnBoardDealt { get; set; }
    public Action<ActionTakenDto>? OnActionTaken { get; set; }
    public Action<StreetAdvancedDto>? OnStreetAdvanced { get; set; }
    public Action<TurnStartedDto>? OnTurnStarted { get; set; }
    public Action<TimerTickDto>? OnTimerTick { get; set; }
    public Action<ShowdownStartedDto>? OnShowdownStarted { get; set; }
    public Action<HandEndedDto>? OnHandEnded { get; set; }
    public Action<SeatChangedDto>? OnSeatChanged { get; set; }
    public Action<SnapshotResyncDto>? OnSnapshotResync { get; set; }
    public Action<ActionRejectedDto>? OnActionRejected { get; set; }

    public void Dispatch(ReadOnlySpan<byte> payload)
    {
        var envelope = JsonSerializer.Deserialize<PokerEventEnvelope>(payload);
        if (envelope is null) return;
        switch (envelope.Kind)
        {
            case PokerEventKinds.HandStarted: Fire(OnHandStarted, envelope.Payload); break;
            case PokerEventKinds.HoleCardsDealt: Fire(OnHoleCardsDealt, envelope.Payload); break;
            case PokerEventKinds.BoardDealt: Fire(OnBoardDealt, envelope.Payload); break;
            case PokerEventKinds.ActionTaken: Fire(OnActionTaken, envelope.Payload); break;
            case PokerEventKinds.StreetAdvanced: Fire(OnStreetAdvanced, envelope.Payload); break;
            case PokerEventKinds.TurnStarted: Fire(OnTurnStarted, envelope.Payload); break;
            case PokerEventKinds.TimerTick: Fire(OnTimerTick, envelope.Payload); break;
            case PokerEventKinds.ShowdownStarted: Fire(OnShowdownStarted, envelope.Payload); break;
            case PokerEventKinds.HandEnded: Fire(OnHandEnded, envelope.Payload); break;
            case PokerEventKinds.SeatChanged: Fire(OnSeatChanged, envelope.Payload); break;
            case PokerEventKinds.SnapshotResync: Fire(OnSnapshotResync, envelope.Payload); break;
            case PokerEventKinds.ActionRejected: Fire(OnActionRejected, envelope.Payload); break;
        }
    }

    private static void Fire<T>(Action<T>? handler, JsonElement payload)
    {
        if (handler is null) return;
        var typed = payload.Deserialize<T>();
        if (typed is not null) handler(typed);
    }
}
