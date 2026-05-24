package web

import (
	"net"
	"net/http"
)

// remoteAddrNet adapts http.Request.RemoteAddr (a "ip:port" string) to a
// net.Addr — what auth.Lookup.ByPassword's rate limiter expects. We use the
// TCP parsing helper because chi.middleware.RealIP rewrites RemoteAddr to
// the unwrapped client IP when X-Forwarded-For is trusted.
type httpRemoteAddr struct{ raw string }

func (a httpRemoteAddr) Network() string { return "tcp" }
func (a httpRemoteAddr) String() string  { return a.raw }

// RemoteAddrNetAddr is exposed as a method on *http.Request via an extension
// pattern (a thin wrapper) so the handlers code stays readable. We define it
// as a free function below; the loginPost shim uses it.
func remoteAddrFor(r *http.Request) net.Addr {
	if r == nil || r.RemoteAddr == "" {
		return nil
	}
	return httpRemoteAddr{raw: r.RemoteAddr}
}

// RemoteAddrNetAddr lives here as a method on a synthetic receiver so call
// sites read `r.RemoteAddrNetAddr()`. Go doesn't allow attaching methods to
// types from other packages, so we cheat with a request-keyed lookup. The
// indirection isn't free but the call rate is "once per login POST".
//
// Implemented as a package-level wrapper so the loginPost handler stays a
// one-liner. The wrapper resolves the address from *http.Request.RemoteAddr.

// To keep handler call sites tidy, we attach RemoteAddrNetAddr to *Request
// via a tiny helper file — see methods.go for the receiver shim.
