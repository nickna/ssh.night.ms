using Microsoft.AspNetCore.Mvc.RazorPages;
using Night.Ms.SshServer.Configuration;

namespace Night.Ms.SshServer.Pages;

public sealed class IndexModel(NightMsOptions options) : PageModel
{
    public bool GoogleConfigured => options.IsGoogleConfigured;
    public bool MicrosoftConfigured => options.IsMicrosoftConfigured;
    public bool AnySsoConfigured => GoogleConfigured || MicrosoftConfigured;
    public string PublicHost { get; } = PublicHostFor(options);

    private static string PublicHostFor(NightMsOptions options)
    {
        if (options.PublicBaseUrl is { } url && Uri.TryCreate(url, UriKind.Absolute, out var u))
        {
            return u.Host;
        }
        return "localhost";
    }
}
