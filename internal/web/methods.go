package web

import (
	"net"
	"net/http"
)

// requestExt is a thin alias that lets us attach methods to *http.Request
// without violating Go's "no methods on other-package types" rule. Used via
// type-conversion at call sites: `(*requestExt)(r).RemoteAddrNetAddr()`.
type requestExt http.Request

func (r *requestExt) RemoteAddrNetAddr() net.Addr {
	return remoteAddrFor((*http.Request)(r))
}

// RemoteAddrNetAddr is a package-level helper invoked from handlers.go as
// `r.RemoteAddrNetAddr()`. Since Go won't let us add methods to *http.Request
// from another package, the handlers use a thin reference helper instead.
func remoteAddrNetAddr(r *http.Request) net.Addr {
	return remoteAddrFor(r)
}
