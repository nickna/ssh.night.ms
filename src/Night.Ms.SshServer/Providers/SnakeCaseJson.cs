using System.Text.Json;

namespace Night.Ms.SshServer.Providers;

// Every JSON-fetching provider in this folder used to declare its own copy of these
// options — same snake_case policy, same intent. Sharing one instance saves the per-call
// allocation and means a policy tweak lands in one place.
internal static class SnakeCaseJson
{
    public static readonly JsonSerializerOptions Options = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
    };
}
