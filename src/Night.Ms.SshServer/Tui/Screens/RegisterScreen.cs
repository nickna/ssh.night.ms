using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshTransport;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// TOFU register flow shown to clients whose fingerprint isn't on file. Sets Result to the
// newly-created User on success; Result remains null if the user closes the screen without
// registering (we then disconnect from BbsSessionRunner).
public sealed class RegisterScreen : Window
{
    private readonly BbsSession _session;
    private readonly AppDbContext _db;

    public RegisterScreen(BbsSession session, AppDbContext db)
    {
        _session = session;
        _db = db;
        Title = "ssh.night.ms — register a handle";

        var greeting = new Label
        {
            X = 2,
            Y = 1,
            Text = "Welcome, stranger. This key isn't on file.",
        };

        var fp = new Label
        {
            X = 2,
            Y = 2,
            Text = $"key  {session.KeyAlgorithm}\nfp   {session.Fingerprint}",
        };

        var prompt = new Label
        {
            X = 2,
            Y = 6,
            Text = "Pick a handle (3–32 chars, letters/digits/_/-):",
        };

        var handleField = new TextField
        {
            X = 2,
            Y = 7,
            Width = 36,
        };

        var status = new Label
        {
            X = 2,
            Y = 9,
            Width = Dim.Fill(2),
            Height = 2,
        };

        var submit = new Button
        {
            X = 2,
            Y = 12,
            Text = "Register",
            IsDefault = true,
        };

        var cancel = new Button
        {
            X = Pos.Right(submit) + 2,
            Y = 12,
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
                Application.RequestStop();
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
            Application.RequestStop();
        };

        Add(greeting, fp, prompt, handleField, status, submit, cancel);

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                Application.RequestStop();
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
        var user = new User
        {
            Handle = handle,
            CreatedAt = now,
            LastSeenAt = now,
        };
        var key = new SshKey
        {
            User = user,
            KeyType = _session.KeyAlgorithm,
            PublicKeyBlob = _session.PublicKeyBlob,
            Fingerprint = _session.Fingerprint,
            Label = "registered at signup",
            AddedAt = now,
        };
        _db.Users.Add(user);
        _db.SshKeys.Add(key);
        await _db.SaveChangesAsync();
        return user;
    }
}
