namespace Night.Ms.SshServer.Diagnostics;

// A point-in-time view of process and host health. Pure data + formatting so the
// same snapshot can render to a TUI label today and a web dashboard later without
// re-deriving the layout.
public sealed record SystemMetricsSnapshot(
    double CpuPercent,
    long WorkingSetBytes,
    long TotalAvailableBytes,
    int Gen0,
    int Gen1,
    int Gen2,
    string DriveName,
    long DriveFreeBytes,
    long DriveTotalBytes,
    TimeSpan Uptime)
{
    public string FormatCompact()
    {
        // DriveName has a trailing separator on both Windows ("C:\") and Linux ("/").
        // Trim the slash on Windows so it reads "C:"; the Linux root stays "/" since
        // TrimEnd leaves a single-char string alone when it would otherwise empty it.
        var drive = DriveName.TrimEnd('\\');
        if (drive.Length > 1) drive = drive.TrimEnd('/');

        return $"CPU {CpuPercent,4:0.0}%  MEM {FormatBytes(WorkingSetBytes)}/{FormatBytes(TotalAvailableBytes)}  " +
               $"GC {Gen0}/{Gen1}/{Gen2}  DISK {drive} {FormatBytes(DriveFreeBytes)} free  " +
               $"UP {FormatUptime(Uptime)}";
    }

    internal static string FormatBytes(long b) => b switch
    {
        >= 1_073_741_824 => $"{b / 1_073_741_824.0:0.#}G",
        >= 1_048_576     => $"{b / 1_048_576.0:0}M",
        >= 1_024         => $"{b / 1_024.0:0}K",
        _                => $"{b}B",
    };

    internal static string FormatUptime(TimeSpan u) =>
        u.TotalDays >= 1   ? $"{(int)u.TotalDays}d{u.Hours}h"
      : u.TotalHours >= 1  ? $"{u.Hours}h{u.Minutes}m"
      :                      $"{u.Minutes}m{u.Seconds}s";
}
