using System.Text.Json;

namespace Night.Ms.SshServer.Doors.Multiplayer;

// One per seat at end-of-hand. UserId is null for CPUs; the ledger silently skips them.
// WageredThisHand is the total chips this seat put into the pot across all streets;
// Payout is what they won (0 for losers). ChipStackAfter is bookkeeping the coordinator
// uses to update the seat hash; the ledger doesn't need it but it ships with the
// movement for atomicity ("settle and stack-update were one logical step").
public sealed record PlayerMovement(
    long? UserId,
    string Handle,
    int WageredThisHand,
    int Payout,
    long ChipStackAfter);

// What MultiplayerGameLedger.SettleHandAsync gets. One per hand at one table; the ledger
// inserts one MultiplayerHand row + one GameRound row per human movement, all inside the
// same transaction.
public sealed record SettleHand(
    string GameKey,
    long TableId,
    long HandNumber,
    IReadOnlyList<PlayerMovement> PlayerMovements,
    JsonDocument HandDetails);

public sealed record SettleOutcome(
    long HandId,
    IReadOnlyList<long> RoundIds);

// Returned by BuyInAsync. The chips amount is mirrored from the request so the caller
// doesn't have to track it separately; the wallet snapshot reflects the post-debit state.
public sealed record BuyInOutcome(long Chips, WalletSnapshot Wallet);
