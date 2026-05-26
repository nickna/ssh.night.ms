package carbonyl

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// allowedSchemes is the closed set of URL schemes Carbonyl is allowed to
// navigate to from the screen-side handoff. Anything else is rejected before
// the child process is even forked.
//
// The intent is to keep a hostile user from using rich mode as a file:// peek
// at the container filesystem or a chrome:// poke at Chromium internals.
// Once the user is inside the running Carbonyl their address bar can navigate
// freely — defense in depth there comes from --host-resolver-rules in args.go.
var allowedSchemes = map[string]struct{}{
	"http":  {},
	"https": {},
	"about": {}, // about:blank only — checked below
	"data":  {},
}

// RejectedURLError is returned by ValidateURL when the URL fails policy. The
// screen renders Reason as a toast so the user understands the gate.
type RejectedURLError struct {
	URL    string
	Reason string
}

func (e *RejectedURLError) Error() string {
	return fmt.Sprintf("rejected URL %q: %s", e.URL, e.Reason)
}

// ValidateURL applies the v1 hardening policy:
//   - non-empty, parseable URL
//   - scheme in the allowlist (http, https, about:blank, data:)
//   - host is not a literal private/loopback/link-local IP
//
// DNS is not resolved — that would (a) take a network round-trip on the hot
// path and (b) wouldn't catch DNS rebinding mid-session anyway. The threat we
// defend against here is a user typing http://192.168.1.1/ to probe the LAN
// from the server's network position. Documented limitation in the plan.
func ValidateURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &RejectedURLError{URL: raw, Reason: "empty URL"}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return &RejectedURLError{URL: raw, Reason: "unparseable: " + err.Error()}
	}
	scheme := strings.ToLower(u.Scheme)
	if _, ok := allowedSchemes[scheme]; !ok {
		return &RejectedURLError{URL: raw, Reason: "scheme " + scheme + " is not allowed"}
	}
	if scheme == "about" {
		// Only about:blank — about:config, about:gpu etc. expose Chromium internals.
		if u.Opaque != "blank" && u.Path != "blank" && raw != "about:blank" {
			return &RejectedURLError{URL: raw, Reason: "only about:blank is allowed"}
		}
		return nil
	}
	if scheme == "data" {
		// data: URLs carry no network identity to constrain. Length-cap to keep
		// the argv from blowing past ARG_MAX (Linux: ~128 KiB). Carbonyl will
		// reject malformed data: itself.
		if len(raw) > 64*1024 {
			return &RejectedURLError{URL: raw[:64] + "...", Reason: "data: URL exceeds 64 KiB"}
		}
		return nil
	}

	host := u.Hostname()
	if host == "" {
		return &RejectedURLError{URL: raw, Reason: "missing host"}
	}
	// Block localhost by name too — --host-resolver-rules covers it inside the
	// running Chromium, but rejecting up front gives a clearer error.
	switch strings.ToLower(host) {
	case "localhost", "ip6-localhost", "ip6-loopback":
		return &RejectedURLError{URL: raw, Reason: "loopback host not allowed"}
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateOrSpecial(ip) {
			return &RejectedURLError{URL: raw, Reason: "private/loopback/link-local IPs not allowed"}
		}
	}
	return nil
}

// isPrivateOrSpecial returns true for IP ranges we never want a user-driven
// browser session to reach from the server. Standard library's IP.IsPrivate /
// IsLoopback / IsLinkLocalUnicast give us most of it; we add a few extras
// that those don't cover (multicast, unspecified, IPv4-mapped IPv6 loopback).
func isPrivateOrSpecial(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	// IPv4-mapped IPv6 that lands in 127/8 after unmapping.
	if v4 := ip.To4(); v4 != nil && v4[0] == 127 {
		return true
	}
	// Reject CGNAT 100.64.0.0/10 — not "private" per RFC1918 but still
	// internal-network space at most ISPs.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}
