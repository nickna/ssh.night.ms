namespace Night.Ms.SshServer.Tui.Chat;

// A single logical chat line — the entire "[12:34:56] alice: hey @bob :wave:" rendered into
// a sequence of pre-styled runs. Word-wrap happens at paint time inside ChatLogView, so a
// long line may occupy multiple display rows. SelfMentioned is a hint the renderer leaves
// on the line so the view can decorate (e.g. ring a bell, paint a marker in the gutter)
// without re-scanning the runs.
internal sealed record ChatLine(IReadOnlyList<ChatRun> Runs, bool SelfMentioned = false);
