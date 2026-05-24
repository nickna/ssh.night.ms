// Package netlimit enforces connection-level DoS controls on the SSH listener:
// a per-IP concurrent connection cap, a per-IP new-connection token bucket,
// and a global cap on in-flight unauthenticated handshakes. All per-IP keys
// collapse IPv6 to /64 so an attacker holding a /64 can't trivially evade the
// limiter by rotating addresses in their prefix.
package netlimit

import (
	"net"
	"net/netip"
)

// CollapseIP returns the per-IP rate-limit key for addr. IPv4 addresses are
// returned as their dotted-quad string. IPv6 addresses are masked to /64
// and returned as a CIDR — the smallest prefix any single end-site is
// guaranteed to be able to allocate to one host, so the limiter treats one
// /64 as one source even when the attacker rotates the lower 64 bits.
//
// Returns "" when addr is nil or cannot be parsed; callers should treat that
// as "skip per-IP gating" (e.g., unix-socket test listeners).
func CollapseIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return collapseHostPort(addr.String())
}

// CollapseIPString is the string-input variant of CollapseIP, used by code
// paths that get an IP as a string (sysop UI commands, audit records) and
// need to produce the same canonical key the cache + Postgres rows use.
// Returns the parsed key plus a nil error on success; the error is non-nil
// only when s parses as neither a host:port nor a bare IP.
func CollapseIPString(s string) (string, error) {
	key := collapseHostPort(s)
	if key == "" {
		return "", &net.AddrError{Err: "unparseable address", Addr: s}
	}
	return key, nil
}

func collapseHostPort(s string) string {
	if s == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		host = s
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if ip.Is4() || ip.Is4In6() {
		return ip.Unmap().String()
	}
	prefix, err := ip.Prefix(64)
	if err != nil {
		return ip.String()
	}
	return prefix.String()
}
