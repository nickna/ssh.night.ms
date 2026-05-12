namespace Night.Ms.SshServer.Tui.Art;

// Curated art bank surfaced by GalleryScreen. v1 has one impl (FileSystemArtGalleryProvider);
// the interface is here so a DB-backed source can drop in later without touching the screen.
//
// List() may be called repeatedly during a session (Enter re-lists for sysop refresh), so it
// should be cheap. Load() returns null on missing or malformed assets rather than throwing —
// the screen handles "we can't show this one" cleanly.
internal interface IArtGalleryProvider
{
    IReadOnlyList<ArtGalleryEntry> List();
    CellGrid? Load(string id);
}
