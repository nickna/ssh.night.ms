package web

import (
	"net"
	"net/http"
)

// remoteAddrNetAddr adapts an *http.Request to the net.Addr the auth-layer
// rate limiter expects. Go won't let us add methods to *http.Request from
// another package, so handlers use this thin helper instead.
func remoteAddrNetAddr(r *http.Request) net.Addr {
	return remoteAddrFor(r)
}
