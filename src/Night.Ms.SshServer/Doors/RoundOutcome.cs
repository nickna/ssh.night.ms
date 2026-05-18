namespace Night.Ms.SshServer.Doors;

// What IGameLedger.PlayRoundAsync returns after a successful round. Wallet is the
// post-debit-and-credit snapshot so the caller can refresh the screen without a second
// query; RoundId points at the freshly-inserted game_rounds row in case the caller wants
// to link to it from a follow-up "view this round's details" UI.
public sealed record RoundOutcome(WalletSnapshot Wallet, long RoundId);
