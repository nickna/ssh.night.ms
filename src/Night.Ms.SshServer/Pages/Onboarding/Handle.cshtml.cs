using System.Security.Claims;
using Microsoft.AspNetCore.Authentication;
using Microsoft.AspNetCore.Authentication.Cookies;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;
using Night.Ms.SshServer.Auth;

namespace Night.Ms.SshServer.Pages.Onboarding;

// GET: reads the External cookie left by the OIDC handler, extracts the SSO context, and
// pre-fills a suggested handle from the email local-part.
// POST: validates the handle, calls IdentityResolutionService to create the User +
// IdentityCredential, signs in with the durable Cookie scheme, clears the External cookie,
// and redirects to /profile.
public sealed class OnboardingHandleModel(
    IdentityResolutionService resolver,
    SysopBootstrap sysopBootstrap) : PageModel
{
    public string? SuggestedHandle { get; set; }
    public string? Email { get; set; }
    public string ProviderDisplay { get; set; } = "your identity provider";
    public string? Error { get; set; }

    public async Task<IActionResult> OnGetAsync()
    {
        var auth = await HttpContext.AuthenticateAsync("External");
        if (!ExternalClaimsReader.TryRead(auth, out var ticket)) return Redirect("/login");
        Email = ticket.Email;
        ProviderDisplay = ticket.Provider.ToString();
        SuggestedHandle = IdentityResolutionService.SuggestHandle(ticket.Email);
        return Page();
    }

    public async Task<IActionResult> OnPostAsync(string handle)
    {
        var auth = await HttpContext.AuthenticateAsync("External");
        if (!ExternalClaimsReader.TryRead(auth, out var ticket)) return Redirect("/login");

        Email = ticket.Email;
        ProviderDisplay = ticket.Provider.ToString();
        SuggestedHandle = handle;

        handle = (handle ?? string.Empty).Trim();
        if (!IsValidHandle(handle))
        {
            Error = "Handle must be 3–32 chars: letters, digits, underscore, or dash.";
            return Page();
        }

        var result = await resolver.CreateUserAndCredentialAsync(
            handle,
            ticket.Provider,
            ticket.Subject,
            ticket.Email,
            ticket.EmailVerified,
            extraMetadata: null,
            isBootstrapHandle: sysopBootstrap.IsBootstrapHandle,
            cancellationToken: HttpContext.RequestAborted);

        if (result is null)
        {
            Error = $"Handle '{handle}' is already taken. Try another.";
            return Page();
        }

        await HttpContext.SignOutAsync("External");
        await HttpContext.SignInAsync(
            CookieAuthenticationDefaults.AuthenticationScheme,
            BuildPrincipal(result.Value.UserId, result.Value.Handle, result.Value.IsSysop));
        return Redirect("/profile");
    }

    private static ClaimsPrincipal BuildPrincipal(long userId, string handle, bool isSysop)
    {
        var claims = new List<Claim>
        {
            new(ClaimTypes.NameIdentifier, userId.ToString()),
            new(ClaimTypes.Name, handle),
        };
        if (isSysop) claims.Add(new Claim(ClaimTypes.Role, "sysop"));
        return new ClaimsPrincipal(new ClaimsIdentity(claims, CookieAuthenticationDefaults.AuthenticationScheme));
    }

    private static bool IsValidHandle(string handle) =>
        handle.Length is >= 3 and <= 32
        && handle.All(c => char.IsAsciiLetterOrDigit(c) || c is '_' or '-');
}
