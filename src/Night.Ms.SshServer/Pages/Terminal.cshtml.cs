using Microsoft.AspNetCore.Authorization;
using Microsoft.AspNetCore.Mvc.RazorPages;

namespace Night.Ms.SshServer.Pages;

[Authorize]
public sealed class TerminalModel : PageModel
{
    public void OnGet() { }
}
