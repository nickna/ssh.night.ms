namespace Night.Ms.SshServer.Tui.Art;

// One discoverable piece in the gallery. Id is opaque to the screen — it round-trips back
// to IArtGalleryProvider.Load when the user navigates onto the piece. For the filesystem
// implementation, Id is the file path; future DB-backed providers can use a primary key
// instead with no screen changes.
internal sealed record ArtGalleryEntry(string Id, string Title);
