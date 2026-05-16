using System.Diagnostics;

namespace Night.Ms.SshServer.Diagnostics;

// Singleton process-health probe shared by anyone who wants to render system metrics.
// Owning the previous CPU sample here (instead of in each consumer) keeps the smoothed
// CPU% consistent across callers — the AdminScreen and a future web dashboard can both
// poll on their own cadence without each having to maintain their own delta state.
public sealed class SystemMetricsCollector
{
    private readonly Process _proc = Process.GetCurrentProcess();
    private readonly Stopwatch _wallClock = Stopwatch.StartNew();
    private readonly object _lock = new();
    private TimeSpan _prevCpu;
    private TimeSpan _prevWall;

    public SystemMetricsSnapshot Sample()
    {
        lock (_lock)
        {
            _proc.Refresh();

            var cpuNow = _proc.TotalProcessorTime;
            var wallNow = _wallClock.Elapsed;

            double cpuPct = 0;
            if (_prevWall != TimeSpan.Zero)
            {
                var wallDelta = (wallNow - _prevWall).TotalMilliseconds;
                if (wallDelta > 0)
                {
                    var cpuDelta = (cpuNow - _prevCpu).TotalMilliseconds;
                    cpuPct = cpuDelta / (wallDelta * Environment.ProcessorCount) * 100.0;
                    cpuPct = Math.Clamp(cpuPct, 0, 100);
                }
            }
            _prevCpu = cpuNow;
            _prevWall = wallNow;

            var gcInfo = GC.GetGCMemoryInfo();

            string driveName;
            long driveFree;
            long driveTotal;
            try
            {
                var root = Path.GetPathRoot(AppContext.BaseDirectory);
                if (string.IsNullOrEmpty(root)) root = "/";
                var drive = new DriveInfo(root);
                driveName = drive.Name;
                driveFree = drive.IsReady ? drive.AvailableFreeSpace : 0;
                driveTotal = drive.IsReady ? drive.TotalSize : 0;
            }
            catch
            {
                // Rare — unreachable drive, permission issue. Keep the rest of the snapshot
                // valid rather than failing the whole sample.
                driveName = "?";
                driveFree = 0;
                driveTotal = 0;
            }

            return new SystemMetricsSnapshot(
                CpuPercent: cpuPct,
                WorkingSetBytes: _proc.WorkingSet64,
                TotalAvailableBytes: gcInfo.TotalAvailableMemoryBytes,
                Gen0: GC.CollectionCount(0),
                Gen1: GC.CollectionCount(1),
                Gen2: GC.CollectionCount(2),
                DriveName: driveName,
                DriveFreeBytes: driveFree,
                DriveTotalBytes: driveTotal,
                Uptime: DateTime.Now - _proc.StartTime);
        }
    }
}
