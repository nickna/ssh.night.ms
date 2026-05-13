using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Tui.Chat;

// One run of contiguous text in a chat message that shares a single (foreground, style)
// attribute. Background is always the screen default — chat doesn't paint backgrounds, mostly
// because terminals look terrible with colored backgrounds spanning a multi-line message.
internal readonly record struct ChatRun(string Text, ArtColor Foreground, ArtStyle Style)
{
    public static ChatRun Plain(string text) => new(text, ArtColor.DefaultForeground, ArtStyle.None);
}
