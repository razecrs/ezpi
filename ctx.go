package ezpi

import (
	"net"
	"net/http"
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

// IP returns the client's IP address.
func (c *Ctx) IP() string { return clientIP(c.Request) }

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

func clientIP(r *http.Request) string {
	// RemoteAddr only: X-Forwarded-For can be faked by anyone.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
