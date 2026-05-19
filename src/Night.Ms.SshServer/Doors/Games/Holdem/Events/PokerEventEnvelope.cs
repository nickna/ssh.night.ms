using System.Text.Json;

namespace Night.Ms.SshServer.Doors.Games.Holdem.Events;

// Single discriminated envelope on the bus. The dispatcher reads Kind, then deserializes
// Payload into the concrete DTO. Mirrors ChatEnvelope so the screen-side wiring follows
// the same pattern.
public sealed record PokerEventEnvelope(string Kind, JsonElement Payload);
