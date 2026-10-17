package ezpi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"
)

type requestIDKey struct{}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// statusWriter remembers the status code for logging and panic handling.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// middleware is the built-in security stack every request goes through.
func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := newRequestID()
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		w := &statusWriter{ResponseWriter: rw}

		defer func() {
			if p := recover(); p != nil {
				a.log.Error("panic", "request_id", id, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				if w.status == 0 {
					a.fail(&Ctx{Request: r, Writer: w, RequestID: id}, fmt.Errorf("panic: %v", p))
				}
			}
			a.log.Info("request", "request_id", id, "method", r.Method, "path", r.URL.Path,
				"status", w.status, "ms", time.Since(start).Milliseconds(), "ip", clientIP(r))
		}()

		h := w.Header()
		h.Set("X-Request-ID", id)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")

		if a.cors(w, r) {
			return // preflight answered
		}

		if ok, wait := a.limiter.allow(clientIP(r)); !ok {
			h.Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			a.fail(&Ctx{Request: r, Writer: w, RequestID: id}, Error(http.StatusTooManyRequests, "slow down"))
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, a.maxBody)
		next.ServeHTTP(w, r)
	})
}

// cors adds CORS headers for allowed origins. It returns true if it fully
// answered a preflight request.
func (a *App) cors(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || !a.origins[origin] {
		return false // not allowed: the browser will block it
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Add("Vary", "Origin")
	if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		h.Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

// route sends the request to the matching handler, with JSON 404/405 errors.
func (a *App) route(w http.ResponseWriter, r *http.Request) {
	h, pattern := a.mux.Handler(r)
	if pattern != "" {
		a.mux.ServeHTTP(w, r) // ServeHTTP (not h) so path values get set
		return
	}
	// No match: ask the mux's fallback whether the path exists for another method.
	c := &Ctx{Request: r, Writer: w, RequestID: requestID(r)}
	probe := &headerOnly{header: http.Header{}}
	h.ServeHTTP(probe, r)
	if probe.status == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", probe.header.Get("Allow"))
		a.fail(c, Error(http.StatusMethodNotAllowed, "method not allowed"))
		return
	}
	a.fail(c, NotFound("not found"))
}

type headerOnly struct {
	header http.Header
	status int
}

func (h *headerOnly) Header() http.Header         { return h.header }
func (h *headerOnly) Write(b []byte) (int, error) { return len(b), nil }
func (h *headerOnly) WriteHeader(code int)        { h.status = code }
