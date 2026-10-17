package ezpi

import (
	"net"
	"net/http"
	"strings"
)

// Ctx gives full access to the request and response.
// Most handlers never need it: add `c *ezpi.Ctx` as a parameter when you do.
type Ctx struct {
	Request   *http.Request
	Writer    http.ResponseWriter
	RequestID string

	status  int
	written bool
}

// Header returns a request header.
func (c *Ctx) Header(key string) string { return c.Request.Header.Get(key) }

// Query returns a query string value.
func (c *Ctx) Query(key string) string { return c.Request.URL.Query().Get(key) }

// IP returns the client's IP address (see App.TrustProxy).
func (c *Ctx) IP() string { return requestMeta(c.Request).ip }

// SetHeader sets a response header.
func (c *Ctx) SetHeader(key, value string) { c.Writer.Header().Set(key, value) }

// Status overrides the success status code (e.g. 202).
func (c *Ctx) Status(code int) { c.status = code }

// Cookie returns a request cookie's value, or "" if missing.
func (c *Ctx) Cookie(name string) string {
	ck, err := c.Request.Cookie(name)
	if err != nil {
		return ""
	}
	return ck.Value
}

// SetCookie sets a cookie with safe defaults: HttpOnly, Secure, SameSite=Lax.
// maxAge is in seconds; use -1 to delete the cookie.
func (c *Ctx) SetCookie(name, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// Redirect sends the client to another URL.
func (c *Ctx) Redirect(url string, code int) {
	c.written = true
	http.Redirect(c.Writer, c.Request, url, code)
}

// clientIP finds the real client IP. X-Forwarded-For is only used after
// App.TrustProxy, because without a proxy in front anyone can fake it.
func (a *App) clientIP(r *http.Request) string {
	if a.trustProxy {
		// Take the rightmost entry: it was added by our own proxy. Entries to
		// its left were sent by the client and can't be trusted.
		xff := r.Header.Values("X-Forwarded-For")
		if len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
