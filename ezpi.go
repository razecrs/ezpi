// Package ezpi makes building secure JSON APIs in Go very easy.
//
//	app := ezpi.New()
//	app.Get("/health", "ok")
//	app.Post("/users", func(u User) (User, error) { return save(u) })
//	app.Start()
//
// Security defaults (headers, timeouts, body limits, rate limiting, strict
// JSON, panic recovery, hidden internal errors) are on out of the box.
package ezpi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// App is an ezpi application. Create one with New.
type App struct {
	Router

	// Private routes require a valid token (see App.Token).
	// The signing secret comes from the EZPI_SECRET env var.
	Private *Router

	mux        *http.ServeMux
	handler    http.Handler
	log        *slog.Logger
	secret     []byte
	dev        bool
	maxBody    int64
	origins    map[string]bool
	limiter    *limiter
	tokenTTL   time.Duration
	onError    func(Err) any
	trustProxy bool
	private    bool // true once a private route is registered
	routes     []*Route
}

// New creates an app with secure defaults turned on.
func New() *App {
	a := &App{
		mux:      http.NewServeMux(),
		log:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
		secret:   []byte(os.Getenv("EZPI_SECRET")),
		dev:      os.Getenv("EZPI_DEV") == "1",
		maxBody:  1 << 20, // 1 MB
		origins:  map[string]bool{},
		limiter:  newLimiter(300),
		tokenTTL: 24 * time.Hour,
	}
	a.Router = Router{app: a}
	a.Private = &Router{app: a, private: true}
	a.handler = a.middleware(http.HandlerFunc(a.route))
	return a
}

// Router registers routes. Use app.Get(...) for public routes and
// app.Private.Get(...) for routes that need a logged-in user.
type Router struct {
	app     *App
	private bool
	prefix  string
}

func (r *Router) Get(path string, handler any) *Route {
	return r.handle(http.MethodGet, path, handler)
}

func (r *Router) Post(path string, handler any) *Route {
	return r.handle(http.MethodPost, path, handler)
}

func (r *Router) Put(path string, handler any) *Route {
	return r.handle(http.MethodPut, path, handler)
}

func (r *Router) Patch(path string, handler any) *Route {
	return r.handle(http.MethodPatch, path, handler)
}

func (r *Router) Delete(path string, handler any) *Route {
	return r.handle(http.MethodDelete, path, handler)
}

// Group returns a router whose routes all start with prefix:
//
//	v1 := app.Group("/v1")
//	v1.Get("/users", listUsers) // GET /v1/users
//
// Groups of app.Private are private too.
func (r *Router) Group(prefix string) *Router {
	return &Router{app: r.app, private: r.private, prefix: joinPath(r.prefix, prefix)}
}

func (r *Router) handle(method, path string, handler any) *Route {
	path = joinPath(r.prefix, path)
	pattern, params := convertPath(path)
	rt := &Route{method: method, path: pattern, private: r.private}
	h, err := r.app.build(rt, params, handler)
	if err != nil {
		panic(fmt.Sprintf("ezpi: %s %s: %v", method, path, err))
	}
	if r.private {
		r.app.private = true
	}
	r.app.mux.Handle(method+" "+pattern, h)
	r.app.routes = append(r.app.routes, rt)
	return rt
}

func joinPath(prefix, path string) string {
	if prefix == "" {
		return path
	}
	prefix = "/" + strings.Trim(prefix, "/")
	if path == "" || path == "/" {
		return prefix
	}
	return prefix + "/" + strings.TrimPrefix(path, "/")
}

// convertPath turns "/users/:id" into "/users/{id}" and returns the param names.
func convertPath(path string) (string, []string) {
	parts := strings.Split(path, "/")
	var params []string
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			params = append(params, p[1:])
			parts[i] = "{" + p[1:] + "}"
		} else if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
			params = append(params, strings.TrimSuffix(p[1:len(p)-1], "..."))
		}
	}
	pattern := strings.Join(parts, "/")
	if pattern == "/" {
		pattern = "/{$}" // "/" should only match the root, not everything
	}
	return pattern, params
}

// SetSecret sets the secret used to sign tokens (instead of EZPI_SECRET).
// It must be at least 32 characters.
func (a *App) SetSecret(secret string) { a.secret = []byte(secret) }

// CORS allows browsers on these origins to call the API.
// By default no cross-origin requests are allowed.
func (a *App) CORS(origins ...string) {
	for _, o := range origins {
		a.origins[strings.TrimSuffix(o, "/")] = true
	}
}

// RateLimit sets how many requests per minute each IP may make. 0 turns it off.
// The default is 300.
func (a *App) RateLimit(perMinute int) { a.limiter = newLimiter(perMinute) }

// TrustProxy tells ezpi it runs behind a reverse proxy or load balancer
// (nginx, Cloudflare, Fly, Render, ...), so the client IP is read from
// X-Forwarded-For. Only turn this on if a proxy is really in front:
// otherwise clients can fake their IP and dodge the rate limit.
func (a *App) TrustProxy() { a.trustProxy = true }

// MaxBody sets the largest request body allowed, in bytes. The default is 1 MB.
func (a *App) MaxBody(bytes int64) { a.maxBody = bytes }

// TokenTTL sets how long tokens from App.Token stay valid. The default is 24h.
func (a *App) TokenTTL(d time.Duration) { a.tokenTTL = d }

// Logger replaces the default logger.
func (a *App) Logger(l *slog.Logger) { a.log = l }

// OnError lets you change the JSON body sent for errors.
func (a *App) OnError(fn func(err Err) any) { a.onError = fn }

// ServeHTTP lets the app be used as a normal http.Handler (handy for tests).
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.handler.ServeHTTP(w, r) }

// Start runs the server on $PORT (default 8080) and stops gracefully on Ctrl+C.
// It exits the program if the server can't start.
func (a *App) Start() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if err := a.Run(":" + port); err != nil {
		a.log.Error("ezpi: server failed", "err", err)
		os.Exit(1)
	}
}

// Run starts the server on addr and blocks until Ctrl+C / SIGTERM,
// then waits up to 10 seconds for in-flight requests to finish.
func (a *App) Run(addr string) error {
	if err := a.check(); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           a,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	a.log.Info("ezpi: listening", "addr", addr, "dev", a.dev)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	a.log.Info("ezpi: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// check refuses to start with an unsafe setup.
func (a *App) check() error {
	if a.private && len(a.secret) < 32 {
		return errors.New("private routes need EZPI_SECRET set to at least 32 characters " +
			"(make one with: openssl rand -hex 32)")
	}
	return nil
}
