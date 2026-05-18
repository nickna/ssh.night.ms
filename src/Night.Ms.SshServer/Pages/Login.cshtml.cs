using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;
using Night.Ms.SshServer.Configuration;

namespace Night.Ms.SshServer.Pages;

public sealed class LoginModel(NightMsOptions options) : PageModel
{
    public bool GoogleConfigured => options.IsGoogleConfigured;
    public bool MicrosoftConfigured => options.IsMicrosoftConfigured;
    public bool AnySsoConfigured => GoogleConfigured || MicrosoftConfigured;
    public string? Flash { get; private set; }

    public void OnGet([FromQuery] string? flash) => Flash = flash;
}
