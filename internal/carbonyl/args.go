package carbonyl

import "strconv"

// LaunchRequest is the per-launch input the screen hands the Runner. URL must
// already have passed ValidateURL — the Runner re-checks defensively but the
// screen toasts a friendlier error.
type LaunchRequest struct {
	URL      string
	UserID   int64
	Handle   string
	RemoteIP string

	// SessionIO is the bridge target — the carbonyl child's PTY is wired to
	// this SSH session via ptybridge. The transport package supplies it via
	// the LaunchCarbonyl closure on session.Session; screens never construct
	// one themselves.
	SessionRef SessionIO

	// InitialCols/Rows are the terminal dimensions at launch. PTY winsize is
	// initialized from these; subsequent resizes flow via SessionRef.WindowChanges.
	InitialCols int
	InitialRows int
}

// buildArgs returns the argv for the carbonyl child process. Always emits the
// hardening flags (no-sandbox + dev-shm + user-data-dir + extension/feature
// disables + host-resolver-rules). User-controlled input lands only at the
// very end as the URL — never anywhere a Chromium flag could be smuggled.
//
// Pure function so the test suite can lock in flag ordering and presence.
func buildArgs(req LaunchRequest, profileDir string, cols, rows int) []string {
	// `cols`/`rows` aren't passed via flag (Carbonyl reads them from the PTY
	// via ioctl(TIOCGWINSZ) on startup) but we accept them so future-proofing
	// for a forced-resolution flag is a no-op at call sites.
	_ = cols
	_ = rows

	args := []string{
		// Mandatory in unprivileged containers — Chromium's setuid sandbox isn't
		// available. The OS-level container boundary is our sandbox here.
		"--no-sandbox",
		// /dev/shm is small in containers; falling back to /tmp avoids crashes
		// on shared-memory allocation under load.
		"--disable-dev-shm-usage",
		// Per-user persistent profile. Cookies, logins, history land here.
		"--user-data-dir=" + profileDir,
		// No extension ecosystem in our headless context.
		"--disable-extensions",
		// Block Chromium's internal network from reaching loopback even if the
		// in-process URL guard misses something (e.g. user typing into Carbonyl's
		// own address bar after launch). Multiple MAP rules chain via comma.
		`--host-resolver-rules=MAP localhost ~NOTFOUND,MAP 127.0.0.1 ~NOTFOUND,MAP [::1] ~NOTFOUND,MAP ip6-localhost ~NOTFOUND`,
		// Window-size hint for the initial render. Carbonyl reads the live
		// PTY size on its own, but seeding this avoids a layout flash on big
		// terminals.
		"--window-size=" + strconv.Itoa(cols) + "," + strconv.Itoa(rows),
	}
	// URL always last so a malformed-but-not-yet-validated string can never
	// be interpreted as a flag.
	args = append(args, req.URL)
	return args
}
