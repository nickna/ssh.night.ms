namespace Night.Ms.SshTransport.Tests;

public class AuthDecisionTests
{
    [Fact]
    public void Known_carries_user_identity()
    {
        AuthDecision decision = new AuthDecision.Known(UserId: 42, Handle: "nick", IsSysop: true);

        var matched = decision switch
        {
            AuthDecision.Known k => $"known:{k.Handle}:{k.UserId}:{k.IsSysop}",
            AuthDecision.Unknown => "unknown",
            AuthDecision.Banned b => $"banned:{b.Reason}",
            _ => "other",
        };

        Assert.Equal("known:nick:42:True", matched);
    }

    [Fact]
    public void Unknown_singleton_is_reference_equal()
    {
        Assert.Same(AuthDecision.Unknown.Instance, AuthDecision.Unknown.Instance);
    }

    [Fact]
    public void Banned_carries_reason()
    {
        AuthDecision decision = new AuthDecision.Banned("policy violation");

        var reason = decision switch
        {
            AuthDecision.Banned b => b.Reason,
            _ => null,
        };

        Assert.Equal("policy violation", reason);
    }

    [Fact]
    public void Records_have_value_equality()
    {
        Assert.Equal(new AuthDecision.Known(1, "nick", true), new AuthDecision.Known(1, "nick", true));
        Assert.NotEqual(new AuthDecision.Known(1, "nick", true), new AuthDecision.Known(1, "nick", false));
        Assert.Equal(new AuthDecision.Banned("x"), new AuthDecision.Banned("x"));
    }
}
