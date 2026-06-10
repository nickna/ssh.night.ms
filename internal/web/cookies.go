package web

import (
	"net/http"
	"time"
)

// secureCookie builds a cookie with the security attributes every nightms
// cookie shares: Path=/, HttpOnly, SameSite=Lax, and Secure per deployment
// config (off behind Cloudflare Flexible TLS, on for direct HTTPS). Keeping
// the attribute set in one constructor means a future policy change (e.g.
// SameSite=Strict) lands everywhere at once.
func secureCookie(name, value string, secure bool, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	}
}

// expiredCookie returns a deletion cookie for name (epoch expiry + MaxAge<0,
// the belt-and-suspenders pair that makes every browser drop it).
func expiredCookie(name string, secure bool) *http.Cookie {
	c := secureCookie(name, "", secure, time.Unix(0, 0))
	c.MaxAge = -1
	return c
}
