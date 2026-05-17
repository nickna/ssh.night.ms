using System.Security.Claims;
using Microsoft.AspNetCore.Antiforgery;
using Microsoft.AspNetCore.Authentication;
using Microsoft.AspNetCore.Authentication.Cookies;
using Microsoft.AspNetCore.Authentication.Google;
using Microsoft.AspNetCore.Authentication.MicrosoftAccount;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.HttpOverrides;
using Microsoft.AspNetCore.Mvc;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Diagnostics;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Hosting;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Providers.Finance;
using Night.Ms.SshServer.Reader;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui;
using Night.Ms.SshServer.Tui.Map;
using Night.Ms.SshServer.Web;
using StackExchange.Redis;

var builder = WebApplication.CreateBuilder(args);

// Single typed view of the NIGHTMS_* env vars + their appsettings aliases. Bound once at
// boot so consumers don't each repeat the env-or-config fallback lookup.
var nightMsOptions = NightMsOptions.FromConfiguration(builder.Configuration);
builder.Services.AddSingleton(nightMsOptions);

// Kestrel binds the web listener; SshHost owns the SSH listener separately. Both live in
// the same process so they share DI, AppDbContext, Redis, NightMsOptions. The web side is
// HTTP only — TLS terminates upstream at the reverse proxy in prod (UseForwardedHeaders
// below restores the client-visible scheme/host on the request).
var httpPort = nightMsOptions.HttpPort is { } p && p is > 0 and <= 65535 ? p : 5080;
builder.WebHost.ConfigureKestrel(opts => opts.ListenAnyIP(httpPort));

builder.Services.Configure<ForwardedHeadersOptions>(o =>
{
    o.ForwardedHeaders = ForwardedHeaders.XForwardedFor | ForwardedHeaders.XForwardedProto;
    o.KnownIPNetworks.Clear();
    o.KnownProxies.Clear();
});

// Web auth: a single cookie scheme owns the persistent session. Google / Microsoft handlers
// are only registered when their (ClientId, ClientSecret) pair is configured — so a dev
// install without SSO credentials still boots cleanly with just the SSH listener and the
// landing page. The handlers expose their default callback paths ("/signin-google" /
// "/signin-microsoft"); our user-facing challenge endpoints will sit under /login/{provider}.
// Two cookie schemes:
//   - "Cookies" (default): the durable session cookie, set after onboarding/sign-in succeeds.
//   - "External":  short-lived cookie that holds the OIDC ticket between the IdP callback and
//                  either (a) immediate sign-in (subject is known) or (b) the onboarding
//                  handle picker. The provider handlers below SignInScheme="External" so the
//                  callback never writes the long-lived cookie before we know the user has a
//                  handle. SignInManager-style flow without Identity itself.
const string ExternalScheme = "External";

var authBuilder = builder.Services.AddAuthentication(opts =>
{
    opts.DefaultScheme = CookieAuthenticationDefaults.AuthenticationScheme;
    opts.DefaultChallengeScheme = CookieAuthenticationDefaults.AuthenticationScheme;
})
.AddCookie(opts =>
{
    opts.Cookie.Name = "nightms-auth";
    opts.Cookie.SameSite = SameSiteMode.Lax;
    opts.Cookie.SecurePolicy = CookieSecurePolicy.SameAsRequest;
    opts.ExpireTimeSpan = TimeSpan.FromDays(30);
    opts.SlidingExpiration = true;
    opts.LoginPath = "/login";
    opts.LogoutPath = "/logout";
    opts.AccessDeniedPath = "/login";
})
.AddCookie(ExternalScheme, opts =>
{
    opts.Cookie.Name = "nightms-ext";
    opts.Cookie.SameSite = SameSiteMode.Lax;
    opts.Cookie.SecurePolicy = CookieSecurePolicy.SameAsRequest;
    opts.ExpireTimeSpan = TimeSpan.FromMinutes(10);
    opts.SlidingExpiration = false;
});

if (nightMsOptions.IsGoogleConfigured)
{
    authBuilder.AddGoogle(opts =>
    {
        opts.ClientId = nightMsOptions.GoogleClientId!;
        opts.ClientSecret = nightMsOptions.GoogleClientSecret!;
        opts.SignInScheme = ExternalScheme;
        opts.SaveTokens = false;
        // openid+email scopes are added by default; the email + email_verified claims arrive
        // on the ticket once the userinfo endpoint responds.
    });
}

if (nightMsOptions.IsMicrosoftConfigured)
{
    authBuilder.AddMicrosoftAccount(opts =>
    {
        opts.ClientId = nightMsOptions.MicrosoftClientId!;
        opts.ClientSecret = nightMsOptions.MicrosoftClientSecret!;
        opts.SignInScheme = ExternalScheme;
        opts.SaveTokens = false;
    });
}

builder.Services.AddAuthorization();
builder.Services.AddAntiforgery();
builder.Services.AddRazorPages();
builder.Services.AddScoped<IdentityResolutionService>();
builder.Services.AddSingleton<ProfilePictureService>();

// Postgres + EF Core. Connection string lives under ConnectionStrings:bbs (set by
// run.ps1, or via appsettings in production). Snake-case convention matches the
// existing migrations.
//
// Register both:
//  - AddDbContext (scoped) — for hosted services + screens that open their own scope
//    (DatabaseInitializer, NewTopicScreen, etc.).
//  - AddDbContextFactory (singleton) — for stateless realtime services that previously
//    opened a fresh scope on every public method just to resolve AppDbContext.
//
// The factory must be a singleton (it has no scope to live in) — which means the shared
// DbContextOptions<AppDbContext> must also be Singleton. The optionsLifetime arg below
// bumps it from the default Scoped to Singleton; runtime DI validation rejects the dual
// registration otherwise.
void ConfigureDb(DbContextOptionsBuilder opt) =>
    opt.UseNpgsql(builder.Configuration.GetConnectionString("bbs"))
       .UseSnakeCaseNamingConvention();
builder.Services.AddDbContext<AppDbContext>(ConfigureDb,
    contextLifetime: ServiceLifetime.Scoped,
    optionsLifetime: ServiceLifetime.Singleton);
builder.Services.AddDbContextFactory<AppDbContext>(ConfigureDb, lifetime: ServiceLifetime.Singleton);

// Redis. ConnectionMultiplexer is thread-safe and intended to be shared as a
// singleton across the whole app.
builder.Services.AddSingleton<IConnectionMultiplexer>(_ =>
    ConnectionMultiplexer.Connect(builder.Configuration.GetConnectionString("redis")
        ?? throw new InvalidOperationException("ConnectionStrings:redis is not configured.")));

builder.Services.AddSingleton<IPasswordHasher, Argon2idPasswordHasher>();
builder.Services.AddSingleton<ILoginRateLimiter, RedisLoginRateLimiter>();
builder.Services.AddSingleton<IDismissedKeyStore, RedisDismissedKeyStore>();
builder.Services.AddSingleton<AuthLookupService>();
builder.Services.AddSingleton<SystemMetricsCollector>();
builder.Services.AddSingleton<IRealtimeBus, RedisRealtimeBus>();
builder.Services.AddSingleton<ChatService>();
builder.Services.AddSingleton<ChatMutationService>();
builder.Services.AddSingleton<PresenceService>();
builder.Services.AddSingleton<ReadStateService>();
builder.Services.AddSingleton<ProfileService>();
builder.Services.AddSingleton<SysopBootstrap>();
builder.Services.AddSingleton<ArtProvider>();
builder.Services.AddSingleton<Night.Ms.SshServer.Tui.Art.IArtGalleryProvider, Night.Ms.SshServer.Tui.Art.FileSystemArtGalleryProvider>();
builder.Services.AddSingleton<Night.Ms.SshServer.Tui.Art.ILobbyIconProvider, Night.Ms.SshServer.Tui.Art.FileSystemLobbyIconProvider>();
builder.Services.AddSingleton<Night.Ms.SshServer.Tui.Art.IWeatherAnimationProvider, Night.Ms.SshServer.Tui.Art.FileSystemWeatherAnimationProvider>();

// Pluggable provider interfaces (M10 follow-up F9). Open-Meteo + Hacker News ship as the
// default no-key implementations; swap in a paid news source or a different weather API by
// re-binding the relevant interface after these calls. Each AddXxx method owns its named
// HttpClient (BaseAddress, User-Agent) so swapping a provider doesn't leak the prior
// vendor's URL into Program.cs.
builder.Services.AddOpenMeteoWeather();
builder.Services.AddNwsWeatherAlerts();
builder.Services.AddHackerNews();
builder.Services.AddOpenMeteoGeocoding();
builder.Services.AddIpApiCoGeolocation();
builder.Services.AddSmartReaderArticleReader();
builder.Services.AddHttpImageFetcher();
builder.Services.AddOsmTileFetcher();
builder.Services.AddOpenFreeMapVectorTileFetcher();
builder.Services.AddYahooFinance();
builder.Services.AddCoinGecko();
builder.Services.AddFrankfurter();
builder.Services.AddCompositeFinanceProvider();
builder.Services.AddYahooFinanceNews();

// DatabaseInitializer must run before SysopBootstrap (the bootstrap needs the schema), and
// SysopBootstrap must run before SshHost so a re-promotion lands before the first login.
builder.Services.AddHostedService<DatabaseInitializer>();
builder.Services.AddHostedService(sp => sp.GetRequiredService<SysopBootstrap>());
builder.Services.AddHostedService<SshHost>();

var app = builder.Build();

app.UseForwardedHeaders();
app.UseStaticFiles();
app.UseWebSockets();
app.UseAuthentication();
app.UseAuthorization();
app.UseAntiforgery();

app.MapGet("/healthz", () => Results.Ok("ok"));
app.MapRazorPages();

// In-browser BBS terminal: cookie-authed WS upgrade, then hand the WebSocket to
// WebSocketTuiSession and run the same BbsSessionRunner that the SSH path uses.
app.MapGet("/ws/bbs", async (HttpContext ctx, IServiceProvider sp, ILoggerFactory lf, AppDbContext db) =>
{
    if (ctx.User.Identity?.IsAuthenticated != true) return Results.Unauthorized();
    if (!ctx.WebSockets.IsWebSocketRequest) return Results.BadRequest("websocket required");

    using var ws = await ctx.WebSockets.AcceptWebSocketAsync();
    var logger = lf.CreateLogger("ws.bbs");
    await using var session = await WebSocketTuiSession.CreateAsync(ws, ctx, db, logger, ctx.RequestAborted);
    if (session is null) return Results.Empty;

    try
    {
        await BbsSessionRunner.RunAsync(sp, session, logger, ctx.RequestAborted);
    }
    catch (OperationCanceledException) { /* client tab closed */ }
    return Results.Empty;
}).RequireAuthorization();

// --- Auth action endpoints ----------------------------------------------------------------
// These are kept as Minimal API endpoints because they don't render a view; each one either
// issues a challenge, reads the External cookie, or completes a sign-in / link / unlink.
// Pages own anything user-facing (Index / Login / Onboarding / Profile).

// POST /login/{provider} — issues an OIDC challenge for the named provider. The provider
// handler signs into the "External" scheme on return and redirects to /signin/complete.
app.MapPost("/login/{provider}", async (string provider, HttpContext ctx, IAntiforgery antiforgery) =>
{
    if (!await ValidateAntiforgeryAsync(ctx, antiforgery)) return Results.Redirect("/login");
    var scheme = ResolveOidcScheme(provider);
    if (scheme is null) return Results.Redirect("/login");
    var props = new AuthenticationProperties { RedirectUri = "/signin/complete" };
    return Results.Challenge(props, [scheme]);
});

// GET /signin/complete — the External cookie now holds the OIDC ticket. Look up the subject;
// if a credential exists or the verified email matches an existing user, sign in immediately
// with the durable Cookies scheme. Otherwise leave the External cookie in place and redirect
// to the onboarding handle picker.
app.MapGet("/signin/complete", async (HttpContext ctx, IdentityResolutionService resolver) =>
{
    var ext = await ctx.AuthenticateAsync(ExternalScheme);
    if (!ExternalClaimsReader.TryRead(ext, out var ticket))
    {
        await ctx.SignOutAsync(ExternalScheme);
        return Results.Redirect("/login");
    }

    var resolution = await resolver.ResolveAsync(
        ticket.Provider, ticket.Subject, ticket.Email, ticket.EmailVerified,
        extraMetadata: null, ctx.RequestAborted);

    switch (resolution)
    {
        case IdentityResolution.Existing(var uid, var handle, var sysop):
        case IdentityResolution.LinkedToExisting(var uid2, var handle2, var sysop2, _):
            var (signedInId, signedInHandle, signedInSysop) = resolution switch
            {
                IdentityResolution.Existing e => (e.UserId, e.Handle, e.IsSysop),
                IdentityResolution.LinkedToExisting l => (l.UserId, l.Handle, l.IsSysop),
                _ => throw new InvalidOperationException(),
            };
            await ctx.SignOutAsync(ExternalScheme);
            await ctx.SignInAsync(CookieAuthenticationDefaults.AuthenticationScheme,
                BuildCookiePrincipal(signedInId, signedInHandle, signedInSysop));
            return Results.Redirect("/profile");

        case IdentityResolution.NewSignup:
            // Keep External cookie so onboarding/handle can read the ticket.
            return Results.Redirect("/onboarding/handle");

        case IdentityResolution.Banned b:
            await ctx.SignOutAsync(ExternalScheme);
            return Results.Content($"This account is banned: {b.Reason}", "text/plain");

        default:
            await ctx.SignOutAsync(ExternalScheme);
            return Results.Redirect("/login");
    }
});

// GET /cancel-signin — wipes a half-finished external ticket and returns to the landing.
app.MapGet("/cancel-signin", async (HttpContext ctx) =>
{
    await ctx.SignOutAsync(ExternalScheme);
    return Results.Redirect("/");
});

// POST /logout — clears the durable cookie.
app.MapPost("/logout", async (HttpContext ctx, IAntiforgery antiforgery) =>
{
    if (!await ValidateAntiforgeryAsync(ctx, antiforgery)) return Results.Redirect("/");
    await ctx.SignOutAsync(CookieAuthenticationDefaults.AuthenticationScheme);
    return Results.Redirect("/");
});

// POST /profile/link/{provider} — logged-in user issues a challenge to add another credential
// to their existing account. The callback returns to /profile/link-complete.
app.MapPost("/profile/link/{provider}", async (string provider, HttpContext ctx, IAntiforgery antiforgery) =>
{
    if (ctx.User.Identity?.IsAuthenticated != true) return Results.Redirect("/login");
    if (!await ValidateAntiforgeryAsync(ctx, antiforgery)) return Results.Redirect("/profile");
    var scheme = ResolveOidcScheme(provider);
    if (scheme is null) return Results.Redirect("/profile");
    var props = new AuthenticationProperties { RedirectUri = "/profile/link-complete" };
    return Results.Challenge(props, [scheme]);
});

// GET /profile/link-complete — runs after the external ticket arrives for a link flow.
app.MapGet("/profile/link-complete", async (HttpContext ctx, IdentityResolutionService resolver) =>
{
    if (ctx.User.Identity?.IsAuthenticated != true) return Results.Redirect("/login");
    var currentUserIdStr = ctx.User.FindFirstValue(ClaimTypes.NameIdentifier);
    if (!long.TryParse(currentUserIdStr, out var currentUserId)) return Results.Redirect("/login");

    var ext = await ctx.AuthenticateAsync(ExternalScheme);
    if (!ExternalClaimsReader.TryRead(ext, out var ticket))
    {
        await ctx.SignOutAsync(ExternalScheme);
        return Results.Redirect("/profile");
    }

    var outcome = await resolver.LinkToUserAsync(
        currentUserId, ticket.Provider, ticket.Subject, ticket.Email, ticket.EmailVerified,
        extraMetadata: null, ctx.RequestAborted);
    await ctx.SignOutAsync(ExternalScheme);

    var flash = outcome switch
    {
        LinkOutcome.Linked => $"Linked {ticket.Provider}.",
        LinkOutcome.AlreadyLinkedToYou => $"{ticket.Provider} is already linked to your account.",
        LinkOutcome.AlreadyLinkedToOther => $"That {ticket.Provider} account is linked to a different handle. Sign in with that one to manage it.",
        _ => null,
    };
    return Results.Redirect($"/profile{(flash is null ? "" : $"?flash={Uri.EscapeDataString(flash)}")}");
});

// POST /profile/password — set or change the user's password. Required for SSH password
// auth and lets web-only users (Google/Microsoft) gain SSH access without first having to
// upload a key. Verifies current password if one is set.
app.MapPost("/profile/password", async (HttpContext ctx, AppDbContext db, IPasswordHasher hasher, NightMsOptions nightMsOptions, IAntiforgery antiforgery, [FromForm] string? current, [FromForm] string? next, [FromForm] string? confirm) =>
{
    if (ctx.User.Identity?.IsAuthenticated != true) return Results.Redirect("/login");
    if (!await ValidateAntiforgeryAsync(ctx, antiforgery)) return Results.Redirect("/profile");
    var idStr = ctx.User.FindFirstValue(ClaimTypes.NameIdentifier);
    if (!long.TryParse(idStr, out var userId)) return Results.Redirect("/login");

    var user = await db.Users.FirstOrDefaultAsync(u => u.Id == userId);
    if (user is null) return Results.Redirect("/login");

    next ??= string.Empty;
    confirm ??= string.Empty;

    var minLen = nightMsOptions.PasswordHashing.MinPasswordLength;
    if (next.Length < minLen)
    {
        return Results.Redirect("/profile?flash=" + Uri.EscapeDataString($"Password must be at least {minLen} characters."));
    }
    if (next != confirm)
    {
        return Results.Redirect("/profile?flash=" + Uri.EscapeDataString("Passwords don't match."));
    }
    if (user.PasswordHash is not null && user.PasswordAlgo is not null)
    {
        if (string.IsNullOrEmpty(current) || !hasher.Verify(current, user.PasswordHash, user.PasswordAlgo))
        {
            return Results.Redirect("/profile?flash=" + Uri.EscapeDataString("Current password is incorrect."));
        }
    }

    var hashed = hasher.Hash(next);
    user.PasswordHash = hashed.Hash;
    user.PasswordAlgo = hashed.Algo;
    user.PasswordUpdatedAt = DateTimeOffset.UtcNow;
    db.AuditLogs.Add(new AuditLog
    {
        ActorId = userId,
        Action = "password.changed",
        TargetType = "user",
        TargetId = userId,
        CreatedAt = DateTimeOffset.UtcNow,
    });
    await db.SaveChangesAsync(ctx.RequestAborted);
    return Results.Redirect("/profile?flash=" + Uri.EscapeDataString("Password updated."));
});

// POST /profile/settings — toggle user preferences that aren't tied to a separate domain
// surface. Houses the "stop asking me to adopt SSH keys" flag and the "require SSH key for
// login" (passwordless) flag. The passwordless toggle is server-side guarded: it refuses to
// flip on when the user has zero SSH keys, mirroring the TUI guard in ProfileEditScreen.
app.MapPost("/profile/settings", async (HttpContext ctx, AppDbContext db, IAntiforgery antiforgery, [FromForm] string? suppress, [FromForm] string? requireKey) =>
{
    if (ctx.User.Identity?.IsAuthenticated != true) return Results.Redirect("/login");
    if (!await ValidateAntiforgeryAsync(ctx, antiforgery)) return Results.Redirect("/profile?tab=settings");
    var idStr = ctx.User.FindFirstValue(ClaimTypes.NameIdentifier);
    if (!long.TryParse(idStr, out var userId)) return Results.Redirect("/login");

    var user = await db.Users.FirstOrDefaultAsync(u => u.Id == userId);
    if (user is null) return Results.Redirect("/login");

    // Unchecked checkboxes don't submit a value at all — absence == false. Treat any
    // non-"true" value as false so the form's hidden-checkbox semantics work either way.
    var requestedSuppress = string.Equals(suppress, "true", StringComparison.OrdinalIgnoreCase);
    var requestedRequireKey = string.Equals(requireKey, "true", StringComparison.OrdinalIgnoreCase);

    var changed = false;
    if (user.SuppressKeyAdoptionPrompts != requestedSuppress)
    {
        user.SuppressKeyAdoptionPrompts = requestedSuppress;
        db.AuditLogs.Add(new AuditLog
        {
            ActorId = userId,
            Action = "settings.key_adoption_prompts_changed",
            TargetType = "user",
            TargetId = userId,
            CreatedAt = DateTimeOffset.UtcNow,
            Details = System.Text.Json.JsonSerializer.SerializeToDocument(new { suppress = requestedSuppress }),
        });
        changed = true;
    }

    if (user.RequireSshKey != requestedRequireKey)
    {
        if (requestedRequireKey)
        {
            // Lockout guard: refuse to enable without ≥1 SSH key on file. Mirrors the TUI
            // pre-save check in ProfileEditScreen.SaveSettingsAsync. The web has no modal so
            // a flash message is the only feedback channel.
            var keyCount = await db.IdentityCredentials
                .CountAsync(c => c.UserId == userId && c.Provider == CredentialProvider.Ssh, ctx.RequestAborted);
            if (keyCount == 0)
            {
                return Results.Redirect("/profile?tab=settings&flash=" + Uri.EscapeDataString(
                    "Add an SSH key first — enabling passwordless mode without one would lock you out."));
            }
        }
        user.RequireSshKey = requestedRequireKey;
        db.AuditLogs.Add(new AuditLog
        {
            ActorId = userId,
            Action = requestedRequireKey ? "user.passwordless.enabled" : "user.passwordless.disabled",
            TargetType = "user",
            TargetId = userId,
            CreatedAt = DateTimeOffset.UtcNow,
            Details = System.Text.Json.JsonSerializer.SerializeToDocument(new { via = "web" }),
        });
        changed = true;
    }

    if (changed)
    {
        await db.SaveChangesAsync(ctx.RequestAborted);
    }
    return Results.Redirect("/profile?tab=settings&flash=" + Uri.EscapeDataString("Settings updated."));
});

// POST /profile/ssh-key — paste an OpenSSH-format public key, parse it, refuse if already
// attached to another user, and write a new IdentityCredential row owned by the caller.
app.MapPost("/profile/ssh-key", async (HttpContext ctx, AppDbContext db, IAntiforgery antiforgery, [FromForm] string? publicKey, [FromForm] string? label) =>
{
    if (ctx.User.Identity?.IsAuthenticated != true) return Results.Redirect("/login");
    if (!await ValidateAntiforgeryAsync(ctx, antiforgery)) return Results.Redirect("/profile");
    var idStr = ctx.User.FindFirstValue(ClaimTypes.NameIdentifier);
    if (!long.TryParse(idStr, out var userId)) return Results.Redirect("/login");

    if (string.IsNullOrWhiteSpace(publicKey))
    {
        return Results.Redirect("/profile?flash=" + Uri.EscapeDataString("Paste an SSH public key."));
    }
    if (!OpenSshPublicKeyParser.TryParse(publicKey, out var parsed))
    {
        return Results.Redirect("/profile?flash=" + Uri.EscapeDataString("Couldn't parse that — paste the contents of an ssh-ed25519 or ssh-rsa .pub file."));
    }

    // (Provider, Subject) is globally unique — refuse if the key is already attached to
    // anyone, including the caller. The friendly error wins over a raw DbUpdateException.
    var existing = await db.IdentityCredentials
        .FirstOrDefaultAsync(c => c.Provider == CredentialProvider.Ssh && c.Subject == parsed.Fingerprint, ctx.RequestAborted);
    if (existing is not null)
    {
        var msg = existing.UserId == userId ? "Key is already on your account." : "Key is already attached to a different account.";
        return Results.Redirect("/profile?flash=" + Uri.EscapeDataString(msg));
    }

    var metadata = System.Text.Json.JsonSerializer.Serialize(new
    {
        algorithm = parsed.Algorithm,
        blob_b64 = Convert.ToBase64String(parsed.Blob),
        comment = parsed.Comment,
    });
    var resolvedLabel = string.IsNullOrWhiteSpace(label)
        ? (string.IsNullOrWhiteSpace(parsed.Comment) ? $"added {DateTimeOffset.UtcNow:yyyy-MM-dd}" : parsed.Comment.Trim())
        : label.Trim();
    db.IdentityCredentials.Add(new IdentityCredential
    {
        UserId = userId,
        Provider = CredentialProvider.Ssh,
        Subject = parsed.Fingerprint,
        Metadata = metadata,
        Label = resolvedLabel,
        CreatedAt = DateTimeOffset.UtcNow,
    });
    db.AuditLogs.Add(new AuditLog
    {
        ActorId = userId,
        Action = "identity.linked",
        TargetType = "identity_credential",
        CreatedAt = DateTimeOffset.UtcNow,
        Details = System.Text.Json.JsonSerializer.SerializeToDocument(new
        {
            provider = "Ssh",
            via = "web-paste",
            fingerprint = parsed.Fingerprint,
        }),
    });
    await db.SaveChangesAsync(ctx.RequestAborted);
    return Results.Redirect("/profile?flash=" + Uri.EscapeDataString("SSH key added."));
});

// POST /profile/unlink/{credentialId} — refuses if it's the user's last credential.
app.MapPost("/profile/unlink/{credentialId:long}", async (long credentialId, HttpContext ctx, IdentityResolutionService resolver, IAntiforgery antiforgery) =>
{
    if (ctx.User.Identity?.IsAuthenticated != true) return Results.Redirect("/login");
    if (!await ValidateAntiforgeryAsync(ctx, antiforgery)) return Results.Redirect("/profile");
    var currentUserIdStr = ctx.User.FindFirstValue(ClaimTypes.NameIdentifier);
    if (!long.TryParse(currentUserIdStr, out var currentUserId)) return Results.Redirect("/login");

    var outcome = await resolver.UnlinkAsync(currentUserId, credentialId, ctx.RequestAborted);
    var flash = outcome switch
    {
        UnlinkOutcome.Removed => "Identity unlinked.",
        UnlinkOutcome.RefusedLastCredential => "Cannot remove your only remaining identity — link another first.",
        UnlinkOutcome.NotFound => "Identity not found.",
        _ => null,
    };
    return Results.Redirect($"/profile{(flash is null ? "" : $"?flash={Uri.EscapeDataString(flash)}")}");
});

// POST /profile/avatar — multipart upload of the user's profile picture. Validates content
// type + size, hands the stream to ProfilePictureService for the center-crop + resize +
// canonical PNG write, and bumps User.ProfilePictureUpdatedAt for cache-busting.
app.MapPost("/profile/avatar", async (HttpContext ctx, ProfilePictureService pfp, [FromForm] IFormFile? image) =>
{
    if (ctx.User.Identity?.IsAuthenticated != true) return Results.Redirect("/login");
    var idStr = ctx.User.FindFirstValue(ClaimTypes.NameIdentifier);
    if (!long.TryParse(idStr, out var userId)) return Results.Redirect("/login");

    if (image is null || image.Length == 0)
    {
        return Results.Redirect("/profile?flash=" + Uri.EscapeDataString("Pick an image file first."));
    }
    if (image.Length > ProfilePictureService.MaxUploadBytes)
    {
        return Results.Redirect("/profile?flash=" + Uri.EscapeDataString("Image too large (max 4 MB)."));
    }

    await using var src = image.OpenReadStream();
    var ok = await pfp.SaveAsync(userId, src, ctx.RequestAborted);
    var flash = ok ? "Picture updated." : "Couldn't process that image. PNG, JPEG, or WebP only.";
    return Results.Redirect("/profile?flash=" + Uri.EscapeDataString(flash));
});

// POST /profile/avatar/delete — removes the current upload (next render shows the identicon).
app.MapPost("/profile/avatar/delete", async (HttpContext ctx, ProfilePictureService pfp, IAntiforgery antiforgery) =>
{
    if (ctx.User.Identity?.IsAuthenticated != true) return Results.Redirect("/login");
    if (!await ValidateAntiforgeryAsync(ctx, antiforgery)) return Results.Redirect("/profile");
    var idStr = ctx.User.FindFirstValue(ClaimTypes.NameIdentifier);
    if (!long.TryParse(idStr, out var userId)) return Results.Redirect("/login");

    var ok = await pfp.DeleteAsync(userId, ctx.RequestAborted);
    return Results.Redirect("/profile?flash=" + Uri.EscapeDataString(ok ? "Picture removed." : "Nothing to remove."));
});

// GET /u/{handle}/avatar — public PNG endpoint. Returns the stored upload when one exists,
// or a freshly-generated identicon. ETag = ticks(updated_at) | "identicon-{handle-lower}" so
// browsers + reverse proxies cache aggressively across the page's avatar references.
app.MapGet("/u/{handle}/avatar", async (string handle, HttpContext ctx, AppDbContext db, ProfilePictureService pfp) =>
{
    var user = await db.Users.AsNoTracking().FirstOrDefaultAsync(u => u.Handle == handle && !u.IsBanned, ctx.RequestAborted);
    if (user is null) return Results.NotFound();

    var etag = user.ProfilePictureUpdatedAt is { } ts
        ? $"\"{ts.UtcTicks}\""
        : $"\"identicon-{handle.ToLowerInvariant()}\"";
    if (ctx.Request.Headers.IfNoneMatch.ToString() == etag)
    {
        return Results.StatusCode(StatusCodes.Status304NotModified);
    }
    ctx.Response.Headers.ETag = etag;
    ctx.Response.Headers.CacheControl = "public, max-age=86400";

    var bytes = await pfp.GetPngBytesAsync(user.Id, user.Handle, ctx.RequestAborted);
    return Results.File(bytes, contentType: "image/png");
});

app.Run();

// Local helpers (file-scoped). Kept here so the wiring stays in one place; if these grow they
// can move into a small Web/AuthEndpoints.cs partial class.
static string? ResolveOidcScheme(string provider) => provider.ToLowerInvariant() switch
{
    "google" => GoogleDefaults.AuthenticationScheme,
    "microsoft" => MicrosoftAccountDefaults.AuthenticationScheme,
    _ => null,
};

// Endpoints with `[FromForm]` parameters are auto-validated by UseAntiforgery() middleware;
// route-only POSTs (link/unlink/logout/etc.) need to call this themselves. Razor `<form
// method="post">` already injects __RequestVerificationToken via FormTagHelper, so the token
// is always in the request body for forms rendered by our pages.
static async ValueTask<bool> ValidateAntiforgeryAsync(HttpContext ctx, IAntiforgery antiforgery)
{
    try { await antiforgery.ValidateRequestAsync(ctx); return true; }
    catch (AntiforgeryValidationException) { return false; }
}

static ClaimsPrincipal BuildCookiePrincipal(long userId, string handle, bool isSysop)
{
    var claims = new List<Claim>
    {
        new(ClaimTypes.NameIdentifier, userId.ToString()),
        new(ClaimTypes.Name, handle),
    };
    if (isSysop) claims.Add(new Claim(ClaimTypes.Role, "sysop"));
    return new ClaimsPrincipal(new ClaimsIdentity(claims, CookieAuthenticationDefaults.AuthenticationScheme));
}
