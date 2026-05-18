using Npgsql;

namespace Night.Ms.SshServer.Persistence;

public static class ConnectionStrings
{
    // Applies pool-sizing defaults to the configured connection string. Operator-set values for
    // Max/Min Pool Size are preserved (the builder reports them via the underlying key collection,
    // not the typed properties, since typed properties always return the default when unset). At
    // our target of hundreds of concurrent users a ceiling of 200 leaves ~1 connection per 2-3
    // sessions — sufficient because each session's DB activity is short and bursty, not held.
    public static string BuildBbs(string? raw)
    {
        if (string.IsNullOrWhiteSpace(raw))
        {
            throw new InvalidOperationException("ConnectionStrings:bbs is not configured.");
        }
        var csb = new NpgsqlConnectionStringBuilder(raw);
        if (!csb.ContainsKey("Maximum Pool Size") && !csb.ContainsKey("MaxPoolSize"))
        {
            csb.MaxPoolSize = 200;
        }
        if (!csb.ContainsKey("Minimum Pool Size") && !csb.ContainsKey("MinPoolSize"))
        {
            csb.MinPoolSize = 10;
        }
        return csb.ConnectionString;
    }
}
