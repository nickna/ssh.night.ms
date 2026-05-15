using System.Net;
using System.Net.Http;

namespace Night.Ms.SshServer.Tests;

// Captures requests and returns canned responses keyed by request-uri substring. Tests
// register (substring → response) pairs in the order they're expected to be matched —
// the first substring that occurs in the request URL wins.
internal sealed class FakeHttpMessageHandler : HttpMessageHandler
{
    private readonly List<(string Match, Func<HttpRequestMessage, HttpResponseMessage> Responder)> _routes = [];
    public List<HttpRequestMessage> Requests { get; } = [];

    public FakeHttpMessageHandler Route(string urlContains, string jsonBody, HttpStatusCode status = HttpStatusCode.OK)
    {
        _routes.Add((urlContains, _ => new HttpResponseMessage(status)
        {
            Content = new StringContent(jsonBody, System.Text.Encoding.UTF8, "application/json"),
        }));
        return this;
    }

    public FakeHttpMessageHandler RouteDynamic(string urlContains, Func<HttpRequestMessage, HttpResponseMessage> responder)
    {
        _routes.Add((urlContains, responder));
        return this;
    }

    public FakeHttpMessageHandler RouteThrowing(string urlContains, Exception ex)
    {
        _routes.Add((urlContains, _ => throw ex));
        return this;
    }

    protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        Requests.Add(request);
        var url = request.RequestUri?.ToString() ?? string.Empty;
        foreach (var (match, responder) in _routes)
        {
            if (url.Contains(match, StringComparison.Ordinal))
            {
                try
                {
                    return Task.FromResult(responder(request));
                }
                catch (Exception ex)
                {
                    return Task.FromException<HttpResponseMessage>(ex);
                }
            }
        }
        return Task.FromResult(new HttpResponseMessage(HttpStatusCode.NotFound) { Content = new StringContent($"unrouted: {url}") });
    }
}

// Mirrors the production AddHttpClient configuration: providers expect BaseAddress to be
// set up-front by the factory (Program.cs does this via AddHttpClient("name", c => ...)).
// We map each known client name to its production base so tests behave identically.
internal sealed class StaticHttpClientFactory(HttpMessageHandler handler) : IHttpClientFactory
{
    private static readonly Dictionary<string, Uri> BaseAddresses = new()
    {
        [Night.Ms.SshServer.Providers.OpenMeteoWeatherProvider.HttpClientName]    = new Uri("https://api.open-meteo.com/"),
        [Night.Ms.SshServer.Providers.HackerNewsProvider.HttpClientName]          = new Uri("https://hacker-news.firebaseio.com/"),
        [Night.Ms.SshServer.Providers.OpenMeteoGeocodingProvider.HttpClientName]  = new Uri("https://geocoding-api.open-meteo.com/"),
        [Night.Ms.SshServer.Providers.IpApiCoGeolocationProvider.HttpClientName]  = new Uri("https://ipapi.co/"),
        [Night.Ms.SshServer.Providers.NwsWeatherAlertProvider.HttpClientName]     = new Uri("https://api.weather.gov/"),
    };

    public HttpClient CreateClient(string name)
    {
        var client = new HttpClient(handler, disposeHandler: false);
        if (BaseAddresses.TryGetValue(name, out var baseAddress))
        {
            client.BaseAddress = baseAddress;
        }
        return client;
    }
}
