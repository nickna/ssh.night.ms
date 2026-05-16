using Night.Ms.SshServer.Diagnostics;

namespace Night.Ms.SshServer.Tests.Diagnostics;

public class SystemMetricsSnapshotTests
{
    [Fact]
    public void FormatCompact_RendersExpectedShape()
    {
        var snap = new SystemMetricsSnapshot(
            CpuPercent: 12.345,
            WorkingSetBytes: 142L * 1024 * 1024,
            TotalAvailableBytes: 512L * 1024 * 1024,
            Gen0: 8,
            Gen1: 2,
            Gen2: 0,
            DriveName: "C:\\",
            DriveFreeBytes: 238L * 1024 * 1024 * 1024,
            DriveTotalBytes: 512L * 1024 * 1024 * 1024,
            Uptime: TimeSpan.FromHours(51) + TimeSpan.FromMinutes(7));

        Assert.Equal(
            "CPU 12.3%  MEM 142M/512M  GC 8/2/0  DISK C: 238G free  UP 2d3h",
            snap.FormatCompact());
    }

    [Fact]
    public void FormatCompact_LinuxRootIsPreserved()
    {
        var snap = new SystemMetricsSnapshot(
            CpuPercent: 0,
            WorkingSetBytes: 0,
            TotalAvailableBytes: 0,
            Gen0: 0, Gen1: 0, Gen2: 0,
            DriveName: "/",
            DriveFreeBytes: 0,
            DriveTotalBytes: 0,
            Uptime: TimeSpan.Zero);

        Assert.Contains("DISK / ", snap.FormatCompact());
    }

    [Theory]
    [InlineData(0L, "0B")]
    [InlineData(512L, "512B")]
    [InlineData(1024L, "1K")]
    [InlineData(2048L, "2K")]
    [InlineData(1_048_576L, "1M")]
    [InlineData(10L * 1024 * 1024, "10M")]
    [InlineData(1_073_741_824L, "1G")]
    [InlineData(1_610_612_736L, "1.5G")]
    public void FormatBytes_BoundaryUnits(long input, string expected)
    {
        Assert.Equal(expected, SystemMetricsSnapshot.FormatBytes(input));
    }

    [Theory]
    [InlineData(0, 0, 45, "0m45s")]
    [InlineData(0, 32, 10, "32m10s")]
    [InlineData(0, 59, 59, "59m59s")]
    [InlineData(1, 0, 0, "1h0m")]
    [InlineData(4, 17, 0, "4h17m")]
    [InlineData(23, 59, 0, "23h59m")]
    [InlineData(24, 0, 0, "1d0h")]
    [InlineData(51, 7, 0, "2d3h")]
    public void FormatUptime_BoundaryUnits(int hours, int minutes, int seconds, string expected)
    {
        var span = new TimeSpan(hours, minutes, seconds);
        Assert.Equal(expected, SystemMetricsSnapshot.FormatUptime(span));
    }
}
