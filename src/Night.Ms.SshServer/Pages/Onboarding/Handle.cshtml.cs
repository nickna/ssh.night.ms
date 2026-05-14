using System.Security.Claims;
using Microsoft.AspNetCore.Authentication;
using Microsoft.AspNetCore.Authentication.Cookies;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Domain;

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
        var (ok, ctx) = await ReadExternalAsync();
        if (!ok) return Redirect("/login");
        Email = ctx.Email;
        ProviderDisplay = ctx.Provider.ToString();
        SuggestedHandle = IdentityResolutionService.SuggestHandle(ctx.Email);
        return Page();
    }

    public async Task<IActionResult> OnPostAsync(string handle)
    {
        var (ok, ctx) = await ReadExternalAsync();
        if (!ok) return Redirect("/login");

        Email = ctx.Email;
        ProviderDisplay = ctx.Provider.ToString();
        SuggestedHandle = handle;

        handle = (handle ?? string.Empty).Trim();
        if (!IsValidHandle(handle))
        {
            Error = "Handle must be 3–32 chars: letters, digits, underscore, or dash.";
            return Page();
        }

        var result = await resolver.CreateUserAndCredentialAsync(
            handle,
            ctx.Provider,
            ctx.Subject,
            ctx.Email,
            ctx.EmailVerified,
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

    private async Task<(bool Ok, ExternalContext Ctx)> ReadExternalAsync()
    {
        var auth = await HttpContext.AuthenticateAsync("External");
        if (!auth.Succeeded || auth.Principal is null) return (false, default);

        var subject = auth.Principal.FindFirstValue(ClaimTypes.NameIdentifier);
        if (string.IsNullOrEmpty(subject)) return (false, default);

        var providerScheme = auth.Properties?.Items["LoginProvider"] ?? auth.Principal.Identity?.AuthenticationType;
        if (!TryMapProvider(providerScheme, out var provider)) return (false, default);

        var email = auth.Principal.FindFirstValue(ClaimTypes.Email);
        var emailVerifiedClaim = auth.Principal.FindFirstValue("email_verified")
                              ?? auth.Principal.FindFirstValue("urn:google:email_verified");
        var emailVerified = provider switch
        {
            // Google explicitly signals verification on the userinfo response.
            CredentialProvider.Google => string.Equals(emailVerifiedClaim, "true", StringComparison.OrdinalIgnoreCase),
            // Microsoft accounts require email verification at signup; the handler does not
            // emit an email_verified claim, so treat the supplied email as verified.
            CredentialProvider.Microsoft => !string.IsNullOrWhiteSpace(email),
            _ => false,
        };
        return (true, new ExternalContext(provider, subject, email, emailVerified));
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

    private static bool TryMapProvider(string? scheme, out CredentialProvider provider)
    {
        if (string.Equals(scheme, "Google", StringComparison.OrdinalIgnoreCase))
        {
            provider = CredentialProvider.Google;
            return true;
        }
        if (string.Equals(scheme, "Microsoft", StringComparison.OrdinalIgnoreCase))
        {
            provider = CredentialProvider.Microsoft;
            return true;
        }
        provider = default;
        return false;
    }

    private static bool IsValidHandle(string handle) =>
        handle.Length is >= 3 and <= 32
        && handle.All(c => char.IsAsciiLetterOrDigit(c) || c is '_' or '-');

    private readonly record struct ExternalContext(
        CredentialProvider Provider,
        string Subject,
        string? Email,
        bool EmailVerified);
}
