using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Night.Ms.SshTransport;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// TOFU register flow shown to clients whose fingerprint isn't on file. Sets Result to the
// newly-created User on success; Result remains null if the user closes the screen without
// registering (we then disconnect from BbsSessionRunner).
public sealed class RegisterScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly string _keyAlgorithm;
    private readonly string _fingerprint;
    private readonly byte[] _publicKeyBlob;
    private readonly AppDbContext _db;
    private readonly SysopBootstrap _sysopBootstrap;

    public RegisterScreen(IApplication app, IServiceProvider services, string keyAlgorithm, string fingerprint, byte[] publicKeyBlob, AppDbContext db, SysopBootstrap sysopBootstrap, ArtProvider art)
        : base(app, services, user: null)
    {
        _app = app;
        _keyAlgorithm = keyAlgorithm;
        _fingerprint = fingerprint;
        _publicKeyBlob = publicKeyBlob;
        _db = db;
        _sysopBootstrap = sysopBootstrap;
        Title = "ssh.night.ms — register a handle";

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

        var greeting = new Label
        {
            X = 2,
            Y = contentTop,
            Text =
                "Welcome to ssh.night.ms. Your SSH key isn't on file here yet —\n" +
                "pick a handle below and it'll be bound to this key forever.\n" +
                "(Lose the key, lose the account. There's no email recovery.)",
        };
        greeting.SetScheme(BbsTheme.Header_);

        var fp = new Label
        {
            X = 2,
            Y = contentTop + 4,
            Text = $"key  {keyAlgorithm}\nfp   {fingerprint}",
        };
        fp.SetScheme(BbsTheme.Faint_);

        var prompt = new Label
        {
            X = 2,
            Y = contentTop + 8,
            Text = "Pick a handle (3–32 chars, letters/digits/_/-):",
        };
        prompt.SetScheme(BbsTheme.Hint);

        var handleField = new TextField
        {
            X = 2,
            Y = contentTop + 9,
            Width = 36,
        };
        handleField.SetScheme(BbsTheme.Input);

        var status = new Label
        {
            X = 2,
            Y = contentTop + 11,
            Width = Dim.Fill(2),
            Height = 2,
        };
        status.SetScheme(BbsTheme.Warning);

        var submit = new Button
        {
            X = 2,
            Y = contentTop + 14,
            Text = "Register",
            IsDefault = true,
        };

        var cancel = new Button
        {
            X = Pos.Right(submit) + 2,
            Y = contentTop + 14,
            Text = "Disconnect",
        };

        submit.Accepting += async (_, e) =>
        {
            e.Handled = true;
            var handle = (handleField.Text ?? string.Empty).Trim();
            if (!IsValidHandle(handle))
            {
                status.Text = "[!] Handle must be 3–32 chars: letters, digits, underscore, dash.";
                return;
            }

            try
            {
                var user = await CreateUserAsync(handle);
                Result = user;
                _app.RequestStop();
            }
            catch (DbUpdateException)
            {
                status.Text = $"[!] Handle '{handle}' is already taken. Try another.";
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

        Add(artView, greeting, fp, prompt, handleField, status, submit, cancel);

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                _app.RequestStop();
                key.Handled = true;
            }
        };
    }

    private static bool IsValidHandle(string handle) =>
        handle.Length is >= 3 and <= 32
        && handle.All(c => char.IsAsciiLetterOrDigit(c) || c is '_' or '-');

    private async Task<User> CreateUserAsync(string handle)
    {
        var now = DateTimeOffset.UtcNow;
        var promoteToSysop = _sysopBootstrap.IsBootstrapHandle(handle);
        var user = new User
        {
            Handle = handle,
            CreatedAt = now,
            LastSeenAt = now,
            IsSysop = promoteToSysop,
        };
        // SSH metadata (algorithm + base64 public key blob) is archived in the credential's
        // jsonb Metadata column — AuthLookupService only needs the fingerprint to authorize
        // a session, but keeping the blob lets a future "show me my registered keys" UI
        // render details without an extra schema.
        var metadata = System.Text.Json.JsonSerializer.Serialize(new
        {
            algorithm = _keyAlgorithm,
            blob_b64 = Convert.ToBase64String(_publicKeyBlob),
        });
        var credential = new IdentityCredential
        {
            User = user,
            Provider = CredentialProvider.Ssh,
            Subject = _fingerprint,
            Metadata = metadata,
            Label = "registered at signup",
            CreatedAt = now,
        };
        _db.Users.Add(user);
        _db.IdentityCredentials.Add(credential);
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
