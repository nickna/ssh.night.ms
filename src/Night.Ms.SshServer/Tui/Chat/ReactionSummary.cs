namespace Night.Ms.SshServer.Tui.Chat;

// Aggregate of who reacted with what for a single message. The view renders these as a
// small footer row under the message: `  👍 3  ❤ 1`. `MineFlags` tells the renderer to
// bold/highlight the emojis the current user contributed to so unreacting feels reversible.
internal sealed record ReactionSummary(string Emoji, int Count, bool ByMe);
