using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Night.Ms.SshTransport;
using Npgsql;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// SSH-side signup screen. Reached when the user connects with a handle the server doesn't
// know yet (AuthDecision.SignupRequired). The handle is prefilled from the SSH username and
// remains editable. The user picks a password (Argon2id-hashed via IPasswordHasher) and, if
// they connected with an SSH key in their agent, can optionally adopt that key as their
// first credential during signup. Sets Result to the created User on success; null on
// cancel — the runner then disconnects.
public sealed class RegisterScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly AuthDecision.SignupRequired _signup;
    private readonly string? _offeredFingerprint;
    private readonly string? _offeredAlgorithm;
    private readonly byte[]? _offeredBlob;
    private readonly AppDbContext _db;
    private readonly SysopBootstrap _sysopBootstrap;
    private readonly IPasswordHasher _hasher;
    private readonly NightMsOptions _options;

    public RegisterScreen(
        IApplication app,
        IServiceProvider services,
        AuthDecision.SignupRequired signup,
        string? offeredFingerprint,
        string? offeredAlgorithm,
        byte[]? offeredBlob,
        string? offeredPassword,
        AppDbContext db,
        SysopBootstrap sysopBootstrap,
        IPasswordHasher hasher,
        NightMsOptions options,
        ArtProvider art)
        : base(app, services, user: null)
    {
        _app = app;
        _signup = signup;
        _offeredFingerprint = offeredFingerprint;
        _offeredAlgorithm = offeredAlgorithm;
        _offeredBlob = offeredBlob;
        _db = db;
        _sysopBootstrap = sysopBootstrap;
        _hasher = hasher;
        _options = options;
        Title = "ssh.night.ms — create your account";

        View artView;
        if (art.IsColor)
        {
            artView = new AnsiArtView { X = 0, Y = 0, Grid = art.Grid };
        }
        else
        {
            var label = new Label { X = 0, Y = 0, Text = art.Art };
            label.SetScheme(BbsTheme.Hint);
            artView = label;
        }
        var contentTop = art.LineCount + 1;

        // Greeting copy adapts to what came through from SSH auth: if we got both handle
        // and password, the form is effectively a confirmation step ("review and Register").
        // Otherwise it's a full signup ask.
        var hasOfferedPassword = !string.IsNullOrEmpty(offeredPassword);
        var greeting = new Label
        {
            X = 2,
            Y = contentTop,
            Text = hasOfferedPassword
                ? "Welcome to ssh.night.ms. We've pre-filled your handle and password\n" +
                  "from what you typed at the SSH prompt. Review and hit Register to\n" +
                  "create your account, or edit anything you'd like to change."
                : "Welcome to ssh.night.ms. Looks like you're new here.\n" +
                  "Pick a handle, set a password, and you're in. If you SSHed in with\n" +
                  "a key, you can adopt it now so future logins skip the password.",
        };
        greeting.SetScheme(BbsTheme.Header_);

        var prompt = new Label
        {
            X = 2,
            Y = contentTop + 4,
            Text = "Handle (3-32 chars, letters/digits/_/-):",
        };
        prompt.SetScheme(BbsTheme.Hint);

        var handleField = new TextField
        {
            X = 2,
            Y = contentTop + 5,
            Width = 36,
            Text = signup.Handle ?? string.Empty,
        };
        handleField.SetScheme(BbsTheme.Input);

        var pwLabel = new Label
        {
            X = 2,
            Y = contentTop + 7,
            Text = $"Password (min {_options.PasswordHashing.MinPasswordLength} chars):",
        };
        pwLabel.SetScheme(BbsTheme.Hint);

        // Pre-fill from the password the user just typed at the SSH prompt (when they got
        // there via password auth). Saves them from re-typing what they already typed —
        // and matches the user's mental model: "I gave the server my password during ssh,
        // it should use that." A short SSH password fails the length check on submit, which
        // is fine: the user sees the error and adjusts.
        var preFilledPassword = offeredPassword ?? string.Empty;
        var pwField = new TextField
        {
            X = 2,
            Y = contentTop + 8,
            Width = 36,
            Secret = true,
            Text = preFilledPassword,
        };
        pwField.SetScheme(BbsTheme.Input);

        var pwConfirmLabel = new Label
        {
            X = 2,
            Y = contentTop + 10,
            Text = "Confirm password:",
        };
        pwConfirmLabel.SetScheme(BbsTheme.Hint);

        var pwConfirmField = new TextField
        {
            X = 2,
            Y = contentTop + 11,
            Width = 36,
            Secret = true,
            Text = preFilledPassword,
        };
        pwConfirmField.SetScheme(BbsTheme.Input);

        // Adopt-key checkbox only shows when the client actually offered a key. Without one,
        // the user is signing up password-only (e.g., a fresh client with no agent keys);
        // they can paste a key later from the web profile.
        var hasOfferedKey = !string.IsNullOrEmpty(_offeredFingerprint) && (_offeredBlob?.Length ?? 0) > 0;
        CheckBox? adoptKey = null;
        Label? keyHintLabel = null;
        if (hasOfferedKey)
        {
            adoptKey = new CheckBox
            {
                X = 2,
                Y = contentTop + 13,
                Text = "_Adopt the SSH key I connected with (recommended)",
                Value = CheckState.Checked,
            };
            keyHintLabel = new Label
            {
                X = 2,
                Y = contentTop + 14,
                Text = $"   key  {_offeredAlgorithm}  {_offeredFingerprint}",
            };
            keyHintLabel.SetScheme(BbsTheme.Faint_);
        }

        var statusY = contentTop + (hasOfferedKey ? 16 : 13);
        var status = new Label
        {
            X = 2,
            Y = statusY,
            Width = Dim.Fill(2),
            Height = 2,
        };
        status.SetScheme(BbsTheme.Warning);

        var submit = new Button
        {
            X = 2,
            Y = statusY + 3,
            Text = "_Register",
            IsDefault = true,
        };

        var cancel = new Button
        {
            X = Pos.Right(submit) + 2,
            Y = statusY + 3,
            Text = "_Disconnect",
        };

        submit.Accepting += async (_, e) =>
        {
            e.Handled = true;
            var handle = (handleField.Text ?? string.Empty).Trim();
            var password = pwField.Text ?? string.Empty;
            var confirm = pwConfirmField.Text ?? string.Empty;

            if (!IsValidHandle(handle))
            {
                status.Text = "[!] Handle must be 3-32 chars: letters, digits, underscore, dash.";
                return;
            }
            if (password.Length < _options.PasswordHashing.MinPasswordLength)
            {
                status.Text = $"[!] Password must be at least {_options.PasswordHashing.MinPasswordLength} characters.";
                return;
            }
            if (password != confirm)
            {
                status.Text = "[!] Passwords don't match.";
                return;
            }

            var adopt = adoptKey?.Value == CheckState.Checked && hasOfferedKey;

            // Pre-check the offered key: friendlier than letting the unique (Provider, Subject)
            // index fire as a DbUpdateException. The unique index is still the source of truth
            // on a race — see the catch below.
            if (adopt && await IsKeyAlreadyAdoptedElsewhereAsync())
            {
                status.Text = "[!] That SSH key is already registered to another account. Uncheck 'Adopt the SSH key' to sign up password-only.";
                return;
            }

            try
            {
                var user = await CreateUserAsync(handle, password, adopt);
                Result = user;
                _app.RequestStop();
            }
            catch (DbUpdateException dbEx) when (dbEx.InnerException is PostgresException pg && pg.SqlState == "23505")
            {
                // 23505 = unique_violation. Use the constraint name to tell the user which
                // collision they actually hit — the old "Handle is already taken" catch-all
                // misreported credential-fingerprint collisions when the user adopted a key
                // already attached to another account.
                status.Text = pg.ConstraintName switch
                {
                    "ix_users_handle" => $"[!] Handle '{handle}' is already taken. Try another.",
                    "ix_identity_credentials_provider_subject" => "[!] That SSH key is already registered to another account. Uncheck 'Adopt the SSH key' to sign up password-only.",
                    _ => $"[!] Registration failed (constraint '{pg.ConstraintName}'). Try again.",
                };
            }
            catch (Exception ex)
            {
                status.Text = $"[!] Registration failed: {ex.Message}";
            }
        };

        cancel.Accepting += (_, e) =>
        {
            e.Handled = true;
            _app.RequestStop();
        };

        Add(artView, greeting, prompt, handleField, pwLabel, pwField, pwConfirmLabel, pwConfirmField);
        if (adoptKey is not null) Add(adoptKey);
        if (keyHintLabel is not null) Add(keyHintLabel);
        Add(status, submit, cancel);

        handleField.SetFocus();
        InstallEscapeHandler();
    }

    private static bool IsValidHandle(string handle) =>
        handle.Length is >= 3 and <= 32
        && handle.All(c => char.IsAsciiLetterOrDigit(c) || c is '_' or '-');

    private async Task<bool> IsKeyAlreadyAdoptedElsewhereAsync()
    {
        if (string.IsNullOrEmpty(_offeredFingerprint)) return false;
        return await _db.IdentityCredentials.AsNoTracking()
            .AnyAsync(c => c.Provider == CredentialProvider.Ssh && c.Subject == _offeredFingerprint);
    }

    private async Task<User> CreateUserAsync(string handle, string password, bool adoptKey)
    {
        var now = DateTimeOffset.UtcNow;
        var promoteToSysop = _sysopBootstrap.IsBootstrapHandle(handle);
        var hashed = _hasher.Hash(password);

        var user = new User
        {
            Handle = handle,
            CreatedAt = now,
            LastSeenAt = now,
            IsSysop = promoteToSysop,
            PasswordHash = hashed.Hash,
            PasswordAlgo = hashed.Algo,
            PasswordUpdatedAt = now,
        };
        _db.Users.Add(user);

        if (adoptKey)
        {
            var metadata = System.Text.Json.JsonSerializer.Serialize(new
            {
                algorithm = _offeredAlgorithm,
                blob_b64 = Convert.ToBase64String(_offeredBlob!),
            });
            var credential = new IdentityCredential
            {
                User = user,
                Provider = CredentialProvider.Ssh,
                Subject = _offeredFingerprint!,
                Metadata = metadata,
                Label = "adopted at signup",
                CreatedAt = now,
                LastUsedAt = now,
            };
            _db.IdentityCredentials.Add(credential);
        }

        await _db.SaveChangesAsync();

        if (promoteToSysop)
        {
            // Add the audit row AFTER SaveChanges so user.Id is populated.
            _db.AuditLogs.Add(new AuditLog
            {
                ActorId = null,
                Action = "sysop.bootstrap",
                TargetType = "user",
                TargetId = user.Id,
                CreatedAt = now,
            });
            await _db.SaveChangesAsync();
        }
        return user;
    }
}
