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

internal sealed class StaticHttpClientFactory(HttpMessageHandler handler) : IHttpClientFactory
{
    public HttpClient CreateClient(string name) => new(handler, disposeHandler: false);
}
