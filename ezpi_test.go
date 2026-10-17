package ezpi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSecret = "0123456789abcdef0123456789abcdef"

type Signup struct {
	Email string `json:"email" ez:"required,email"`
	Name  string `json:"name" ez:"required,max=5"`
}

func newApp() *App {
	a := New()
	a.Logger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.SetSecret(testSecret)
	return a
}

type resp struct {
	code int
	body map[string]any
	raw  string
	hdr  http.Header
}

func do(t *testing.T, a *App, method, path, body string, headers ...string) resp {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	r := resp{code: w.Code, raw: w.Body.String(), hdr: w.Header()}
	_ = json.Unmarshal(w.Body.Bytes(), &r.body)
	return r
}

func expect(t *testing.T, r resp, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("status = %d, want %d (body: %s)", r.code, code, r.raw)
	}
}

func TestStaticValue(t *testing.T) {
	a := newApp()
	a.Get("/health", "alive")
	r := do(t, a, "GET", "/health", "")
	expect(t, r, 200)
	if strings.TrimSpace(r.raw) != `"alive"` {
		t.Fatalf("body = %s", r.raw)
	}
}

func TestBodyAndValidation(t *testing.T) {
	a := newApp()
	a.Post("/users", func(u Signup) Signup { return u })

	r := do(t, a, "POST", "/users", `{"email":"a@b.com","name":"sam"}`)
	expect(t, r, 201)
	if r.body["name"] != "sam" {
		t.Fatalf("body = %s", r.raw)
	}

	r = do(t, a, "POST", "/users", `{"email":"nope","name":"toolongname"}`)
	expect(t, r, 400)
	fields := r.body["fields"].(map[string]any)
	if fields["email"] != "must be a valid email" || fields["name"] != "must be at most 5 characters" {
		t.Fatalf("fields = %v", fields)
	}

	r = do(t, a, "POST", "/users", "")
	expect(t, r, 400)
	if r.body["fields"].(map[string]any)["email"] != "is required" {
		t.Fatalf("body = %s", r.raw)
	}
}

func TestStrictJSON(t *testing.T) {
	a := newApp()
	a.Post("/users", func(u Signup) Signup { return u })

	expect(t, do(t, a, "POST", "/users", `{"email":"a@b.com","name":"x","admin":true}`), 400)
	expect(t, do(t, a, "POST", "/users", `{"email":"a@b.com","name":"x"}{}`), 400)
	expect(t, do(t, a, "POST", "/users", `{bad`), 400)
	r := do(t, a, "POST", "/users", `{"email":"a@b.com","name":"x"}`, "Content-Type", "text/plain")
	expect(t, r, 415)
}

func TestBodyTooLarge(t *testing.T) {
	a := newApp()
	a.MaxBody(10)
	a.Post("/users", func(u Signup) Signup { return u })
	expect(t, do(t, a, "POST", "/users", `{"email":"a@b.com","name":"x"}`), 413)
}

func TestPathParams(t *testing.T) {
	a := newApp()
	a.Get("/users/:id/posts/:slug", func(id int, slug string) string {
		return fmt.Sprintf("%d-%s", id, slug)
	})
	r := do(t, a, "GET", "/users/7/posts/hi", "")
	expect(t, r, 200)
	if strings.TrimSpace(r.raw) != `"7-hi"` {
		t.Fatalf("body = %s", r.raw)
	}
	r = do(t, a, "GET", "/users/abc/posts/hi", "")
	expect(t, r, 400)
	if r.body["error"] != "id must be a number" {
		t.Fatalf("body = %s", r.raw)
	}
}

func TestQueryBinding(t *testing.T) {
	type Search struct {
		Q     string `ez:"required"`
		Limit int    `ez:"max=50"`
	}
	a := newApp()
	a.Get("/search", func(s Search) Search { return s })
	r := do(t, a, "GET", "/search?q=go&limit=10", "")
	expect(t, r, 200)
	if r.body["Q"] != "go" || r.body["Limit"] != float64(10) {
		t.Fatalf("body = %s", r.raw)
	}
	expect(t, do(t, a, "GET", "/search?q=go&limit=99", ""), 400)
	expect(t, do(t, a, "GET", "/search?limit=1", ""), 400)
}

func TestErrors(t *testing.T) {
	a := newApp()
	a.Get("/missing", func() (string, error) { return "", NotFound("user not found") })
	a.Get("/wrapped", func() (string, error) {
		return "", fmt.Errorf("loading: %w", Conflict("email exists"))
	})
	a.Get("/boom", func() (string, error) { return "", errors.New("pq: password=hunter2 failed") })
	a.Get("/panic", func() string { panic("oh no") })
	a.Delete("/thing", func() error { return nil })

	r := do(t, a, "GET", "/missing", "")
	expect(t, r, 404)
	if r.body["error"] != "user not found" {
		t.Fatalf("body = %s", r.raw)
	}

	expect(t, do(t, a, "GET", "/wrapped", ""), 409)

	r = do(t, a, "GET", "/boom", "")
	expect(t, r, 500)
	if strings.Contains(r.raw, "hunter2") || r.body["request_id"] == "" {
		t.Fatalf("internal error leaked or no request_id: %s", r.raw)
	}

	r = do(t, a, "GET", "/panic", "")
	expect(t, r, 500)
	if strings.Contains(r.raw, "oh no") {
		t.Fatalf("panic leaked: %s", r.raw)
	}

	expect(t, do(t, a, "DELETE", "/thing", ""), 204)
}

func TestDevModeShowsErrors(t *testing.T) {
	a := newApp()
	a.dev = true
	a.Get("/boom", func() error { return errors.New("db down") })
	if r := do(t, a, "GET", "/boom", ""); r.body["error"] != "db down" {
		t.Fatalf("body = %s", r.raw)
	}
}

func TestOnError(t *testing.T) {
	a := newApp()
	a.OnError(func(e Err) any { return map[string]any{"ok": false, "msg": e.Message} })
	a.Get("/x", func() error { return BadRequest("nope") })
	r := do(t, a, "GET", "/x", "")
	expect(t, r, 400)
	if r.body["msg"] != "nope" || r.body["ok"] != false {
		t.Fatalf("body = %s", r.raw)
	}
}

func TestNotFoundAndMethodNotAllowed(t *testing.T) {
	a := newApp()
	a.Get("/users", "list")
	r := do(t, a, "GET", "/nope", "")
	expect(t, r, 404)
	if r.body["error"] != "not found" {
		t.Fatalf("body = %s", r.raw)
	}
	r = do(t, a, "POST", "/users", "")
	expect(t, r, 405)
	if r.body["error"] != "method not allowed" || r.hdr.Get("Allow") == "" {
		t.Fatalf("body = %s, allow = %q", r.raw, r.hdr.Get("Allow"))
	}
}

func TestPrivateRoutes(t *testing.T) {
	a := newApp()
	a.Private.Get("/me", func(u User) string { return "hi " + u.ID })

	expect(t, do(t, a, "GET", "/me", ""), 401)
	expect(t, do(t, a, "GET", "/me", "", "Authorization", "Bearer garbage"), 401)

	tok, err := a.Token("42")
	if err != nil {
		t.Fatal(err)
	}
	r := do(t, a, "GET", "/me", "", "Authorization", "Bearer "+tok)
	expect(t, r, 200)
	if strings.TrimSpace(r.raw) != `"hi 42"` || r.hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("body = %s", r.raw)
	}

	// Tampered payload.
	parts := strings.Split(tok, ".")
	parts[1] = b64.EncodeToString([]byte(`{"sub":"1","exp":9999999999}`))
	expect(t, do(t, a, "GET", "/me", "", "Authorization", "Bearer "+strings.Join(parts, ".")), 401)

	// alg=none.
	none := b64.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + parts[1] + "."
	expect(t, do(t, a, "GET", "/me", "", "Authorization", "Bearer "+none), 401)

	// Token signed with another secret.
	other := newApp()
	other.SetSecret(strings.Repeat("z", 32))
	tok2, _ := other.Token("42")
	expect(t, do(t, a, "GET", "/me", "", "Authorization", "Bearer "+tok2), 401)

	// Expired.
	a.TokenTTL(-1)
	old, _ := a.Token("42")
	expect(t, do(t, a, "GET", "/me", "", "Authorization", "Bearer "+old), 401)
}

func TestWeakSecretRefusesToStart(t *testing.T) {
	a := newApp()
	a.SetSecret("short")
	a.Private.Get("/me", func(u User) string { return u.ID })
	if err := a.check(); err == nil {
		t.Fatal("expected error for weak secret")
	}
	if _, err := a.Token("1"); err == nil {
		t.Fatal("expected Token to refuse weak secret")
	}
	expect(t, do(t, a, "GET", "/me", "", "Authorization", "Bearer x"), 401)
}

func TestSecurityHeaders(t *testing.T) {
	a := newApp()
	a.Get("/", "home")
	r := do(t, a, "GET", "/", "")
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy",
		"Strict-Transport-Security", "Referrer-Policy", "X-Request-ID"} {
		if r.hdr.Get(h) == "" {
			t.Errorf("missing header %s", h)
		}
	}
	// "/" only matches the root.
	expect(t, do(t, a, "GET", "/other", ""), 404)
}

func TestCORS(t *testing.T) {
	a := newApp()
	a.CORS("https://good.com")
	a.Get("/x", "ok")

	r := do(t, a, "GET", "/x", "", "Origin", "https://evil.com")
	if r.hdr.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("evil origin allowed")
	}
	r = do(t, a, "GET", "/x", "", "Origin", "https://good.com")
	if r.hdr.Get("Access-Control-Allow-Origin") != "https://good.com" {
		t.Fatal("good origin not allowed")
	}
	r = do(t, a, "OPTIONS", "/x", "", "Origin", "https://good.com", "Access-Control-Request-Method", "POST")
	expect(t, r, 204)
}

func TestRateLimit(t *testing.T) {
	a := newApp()
	a.RateLimit(3)
	a.Get("/x", "ok")
	for i := 0; i < 3; i++ {
		expect(t, do(t, a, "GET", "/x", ""), 200)
	}
	r := do(t, a, "GET", "/x", "")
	expect(t, r, 429)
	if r.hdr.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}

func TestCtxAndStatus(t *testing.T) {
	a := newApp()
	a.Post("/jobs", func(c *Ctx) string {
		c.Status(202)
		c.SetHeader("X-Job", "1")
		return c.Header("X-Name")
	})
	r := do(t, a, "POST", "/jobs", "", "X-Name", "sam")
	expect(t, r, 202)
	if r.hdr.Get("X-Job") != "1" || strings.TrimSpace(r.raw) != `"sam"` {
		t.Fatalf("body = %s", r.raw)
	}
}

func TestNestedValidation(t *testing.T) {
	type Item struct {
		SKU string `json:"sku" ez:"required"`
	}
	type Order struct {
		Items []Item `json:"items" ez:"required,min=1"`
		Kind  string `json:"kind" ez:"oneof=pickup|delivery"`
	}
	a := newApp()
	a.Post("/orders", func(o Order) Order { return o })
	r := do(t, a, "POST", "/orders", `{"items":[{"sku":"a"},{"sku":""}],"kind":"drone"}`)
	expect(t, r, 400)
	f := r.body["fields"].(map[string]any)
	if f["items[1].sku"] != "is required" || f["kind"] != "must be one of: pickup, delivery" {
		t.Fatalf("fields = %v", f)
	}
}

func TestBadHandlersPanicAtStartup(t *testing.T) {
	cases := map[string]func(a *App){
		"param count":    func(a *App) { a.Get("/u/:id", func() string { return "" }) },
		"extra scalar":   func(a *App) { a.Get("/u", func(id int) string { return "" }) },
		"user on public": func(a *App) { a.Get("/me", func(u User) string { return "" }) },
		"bad returns":    func(a *App) { a.Get("/x", func() (string, int) { return "", 0 }) },
		"two structs":    func(a *App) { a.Post("/x", func(a, b Signup) {}) },
		"bad tag": func(a *App) {
			type T struct {
				X string `ez:"requird"`
			}
			a.Post("/x", func(T) {})
		},
	}
	for name, register := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			register(newApp())
		})
	}
}
