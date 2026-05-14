using Microsoft.AspNetCore.Mvc.RazorPages;
using Night.Ms.SshServer.Configuration;

namespace Night.Ms.SshServer.Pages;

public sealed class IndexModel(NightMsOptions options) : PageModel
{
    public bool GoogleConfigured => options.IsGoogleConfigured;
    public bool MicrosoftConfigured => options.IsMicrosoftConfigured;
    public bool AnySsoConfigured => GoogleConfigured || MicrosoftConfigured;
    public string SshHost { get; } = options.SshHost ?? "localhost";
    public int SshPort { get; } = options.SshPortPublic ?? options.SshPort ?? 2222;
}
