using System.Security.Claims;
using Microsoft.AspNetCore.Authentication;
using Microsoft.AspNetCore.Authentication.Google;
using Microsoft.AspNetCore.Authentication.MicrosoftAccount;
using Night.Ms.SshServer.Domain;

namespace Night.Ms.SshServer.Auth;

// Single source of truth for parsing the External-cookie ticket left by the Google /
// Microsoft OIDC handlers. Lives outside Program.cs and the page models because the
// LoginProvider lookup is a dictionary indexer that throws on missing keys — when this
// logic was duplicated across two call sites we had to fix the same crash twice.
public static class ExternalClaimsReader
{
    public readonly record struct ExternalTicket(
        CredentialProvider Provider,
        string Subject,
        string? Email,
        bool EmailVerified);

    public static bool TryRead(AuthenticateResult auth, out ExternalTicket ticket)
    {
        ticket = default;
        if (!auth.Succeeded || auth.Principal is null) return false;

        var principal = auth.Principal;
        var subject = principal.FindFirstValue(ClaimTypes.NameIdentifier);
        if (string.IsNullOrEmpty(subject)) return false;

        // Items["LoginProvider"] is a dictionary indexer that throws on missing keys; the
        // OAuth handler doesn't reliably populate it. AuthenticationType is the durable
        // signal ("Google" / "Microsoft") set by the remote handler on the ticket.
        string? scheme = null;
        if (auth.Properties is { } props && props.Items.TryGetValue("LoginProvider", out var lp))
            scheme = lp;
        scheme ??= principal.Identity?.AuthenticationType;

        CredentialProvider provider;
        if (string.Equals(scheme, GoogleDefaults.AuthenticationScheme, StringComparison.OrdinalIgnoreCase))
            provider = CredentialProvider.Google;
        else if (string.Equals(scheme, MicrosoftAccountDefaults.AuthenticationScheme, StringComparison.OrdinalIgnoreCase))
            provider = CredentialProvider.Microsoft;
        else
            return false;

        var email = principal.FindFirstValue(ClaimTypes.Email);
        var emailVerified = provider switch
        {
            CredentialProvider.Google =>
                string.Equals(principal.FindFirstValue("email_verified"), "true", StringComparison.OrdinalIgnoreCase)
                || string.Equals(principal.FindFirstValue("urn:google:email_verified"), "true", StringComparison.OrdinalIgnoreCase),
            // Microsoft accounts require verified email at signup; the handler does not
            // surface a separate email_verified claim. Treat the presence of an email as
            // verified.
            CredentialProvider.Microsoft => !string.IsNullOrWhiteSpace(email),
            _ => false,
        };

        ticket = new ExternalTicket(provider, subject, email, emailVerified);
        return true;
    }
}
