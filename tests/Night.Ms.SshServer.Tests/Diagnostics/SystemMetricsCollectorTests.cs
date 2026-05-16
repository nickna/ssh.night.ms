using Night.Ms.SshServer.Diagnostics;

namespace Night.Ms.SshServer.Tests.Diagnostics;

public class SystemMetricsCollectorTests
{
    [Fact]
    public async Task Sample_DoesNotThrow_AndReportsSaneValues()
    {
        var collector = new SystemMetricsCollector();

        // First sample seeds the prev-CPU baseline; CPU% is expected to be 0.
        var first = collector.Sample();
        Assert.Equal(0, first.CpuPercent);
        Assert.True(first.WorkingSetBytes > 0, "working set should be positive for a live process");
        Assert.True(first.TotalAvailableBytes > 0, "managed memory ceiling should be positive");
        Assert.False(string.IsNullOrEmpty(first.DriveName));

        // Sample again after a small delay so the collector has a real wall-clock delta to
        // divide by. The CPU% must land in [0, 100] regardless of host load.
        await Task.Delay(150);
        var second = collector.Sample();
        Assert.InRange(second.CpuPercent, 0, 100);
        Assert.True(second.Uptime > TimeSpan.Zero);
    }
}
