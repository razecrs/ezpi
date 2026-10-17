# ezpi

Very easy, secure-by-default JSON APIs for Go. Write normal functions and ezpi handles the rest.

```go
package main

import "github.com/razecrs/ezpi"

type User struct {
    Email string `json:"email" ez:"required,email"`
    Name  string `json:"name"  ez:"required,max=50"`
}

func main() {
    app := ezpi.New()

    app.Get("/health", "ok")

    app.Post("/users", func(u User) User {
        return u
    })

    app.Get("/users/:id", func(id int) (User, error) {
        return User{}, ezpi.NotFound("user not found")
    })

    app.Private.Get("/me", func(user ezpi.User) string {
        return "hi " + user.ID
    })

    app.Start()
}
```

```sh
go get github.com/razecrs/ezpi
```

## Handlers are just functions

Ask for what you need in the function's parameters:

| Parameter | What you get |
|---|---|
| a struct | the JSON body (POST/PUT/PATCH) or the query string (GET/DELETE), already validated |
| `int`, `string`, ... | URL params, in order: `/users/:id/posts/:slug` → `func(id int, slug string)` |
| `ezpi.User` | the logged-in user (only on `app.Private` routes) |
| `context.Context` | the request context, for DB calls |
| `*ezpi.Ctx` | headers, cookies, IP, status code, redirects: anything else |

Return whatever you want to send:

| Return | Response |
|---|---|
| a value | JSON, `200` (`201` for POST) |
| `(value, error)` | the value, or the error |
| `error` | `204` if nil, or the error |
| nothing | `204` |

You can also pass a plain value instead of a function: `app.Get("/health", "ok")`.

Bad handlers (wrong param count, typo in an `ez` tag, ...) crash at startup with a clear message, never in the middle of a live request.

## Groups

```go
v1 := app.Group("/v1")
v1.Get("/users", listUsers)            // GET /v1/users

admin := app.Private.Group("/admin")   // every route here needs a token
admin.Get("/stats", stats)             // GET /admin/stats
```

## API docs, generated for you

```go
app.Docs("/docs")

app.Get("/todos/:id", getTodo).Doc("Get a todo")
app.Post("/todos", createTodo).Doc("Create a todo").Tags("todos")
```

Open `/docs` for interactive Swagger UI docs, or grab the spec at `/docs/openapi.json` (OpenAPI 3.1). Everything comes from your code: param types, request and response structs, `ez` rules (`max=100` becomes `maxLength: 100`), status codes, and which routes need a token. The docs can't drift from the code.

Docs are **off by default**, since they map out your whole API. Decide whether you want them public in production. The docs page loads Swagger UI from jsdelivr under a strict CSP that only allows that CDN and the page's own script.

## Errors

```go
return User{}, ezpi.NotFound("user not found")   // 404 {"error":"user not found"}
return User{}, ezpi.BadRequest("name is taken")  // 400
return User{}, ezpi.Unauthorized("...")          // 401
return User{}, ezpi.Forbidden("not your post")   // 403
return User{}, ezpi.Conflict("email exists")     // 409
return User{}, ezpi.Error(418, "i'm a teapot")   // any status
```

**The rule:** errors from the `ezpi.` helpers are shown to the user (even when wrapped with `%w`). Any other error is **hidden** and logged:

```json
{ "error": "something went wrong", "request_id": "a1b2c3d4e5f6a7b8" }
```

```
level=ERROR msg="handler error" request_id=a1b2c3d4e5f6a7b8 path=/users/5 err="pq: connection refused"
```

Panics are handled the same way, and the server keeps running. Set `EZPI_DEV=1` to see real error messages in responses while developing.

To change the error JSON shape:

```go
app.OnError(func(e ezpi.Err) any {
    return map[string]any{"ok": false, "msg": e.Message}
})
```

## Validation

Add `ez` tags to your struct fields:

| Rule | Meaning |
|---|---|
| `required` | must not be empty |
| `email` | valid email address |
| `url` | http(s) URL |
| `min=N` / `max=N` | text length, number value, or list size |
| `oneof=a\|b\|c` | one of the listed values |

Nested structs and lists of structs are checked too. Failures look like:

```json
{
  "error": "validation failed",
  "fields": { "email": "must be a valid email", "items[1].sku": "is required" }
}
```

## Auth

```go
app.Post("/login", func(l Login) (map[string]string, error) {
    if !checkPassword(l) {
        return nil, ezpi.Unauthorized("wrong username or password")
    }
    token, err := app.Token(l.Username)
    return map[string]string{"token": token}, err
})

app.Private.Get("/me", func(user ezpi.User) string { return user.ID })
```

Clients send `Authorization: Bearer <token>`. Tokens are HS256 JWTs signed with `EZPI_SECRET`, which must be at least 32 characters (`openssl rand -hex 32`). The app **refuses to start** with private routes and a weak or missing secret. Tokens last 24h by default (`app.TokenTTL(...)`).

## Security that's on by default

- **Headers:** HSTS, CSP, `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy`, COOP
- **Server timeouts** (read, write, idle, headers): protects against slow-client attacks
- **1 MB body limit**, configurable with `app.MaxBody(...)`
- **Rate limit:** 300 requests per minute per IP, configurable with `app.RateLimit(...)`; `0` turns it off
- **Strict JSON:** unknown fields, trailing data and wrong types are rejected; `Content-Type: application/json` is required, which also blocks CSRF-style form posts
- **CORS denied** unless you allow origins: `app.CORS("https://myapp.com")`
- **Internal errors and panics hidden**, with a request ID to trace them in the logs
- **Safe cookies:** `c.SetCookie` defaults to HttpOnly, Secure and SameSite=Lax
- **Constant-time token checks**, with the algorithm pinned to HS256 (no `alg: none` tricks)
- **`Cache-Control: no-store`** on private responses
- **Graceful shutdown** on Ctrl+C or SIGTERM

## Behind a proxy

If you deploy behind nginx, Cloudflare, a load balancer, Fly, Render and so on, tell ezpi so it reads the real client IP from `X-Forwarded-For`:

```go
app.TrustProxy()
```

Without this, every request looks like it came from your proxy, so the rate limit would hit all users at once. **Only** turn it on when a proxy really is in front. Otherwise clients can fake their IP and dodge the rate limit. ezpi uses the rightmost `X-Forwarded-For` entry, which is the one your proxy added, so values sent by the client are ignored.

## Running

`app.Start()` listens on `$PORT` (default `8080`). Use `app.Run(":3000")` if you want the error back instead of exiting.

The app is also a plain `http.Handler`, so you can test it with `httptest`:

```go
w := httptest.NewRecorder()
app.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
```

See [`examples/basic`](examples/basic/main.go) for a full todo API with login and docs.

## Notes

- Go can't see parameter names, so URL params are matched **by order**.
- Add `json:"name"` tags if you want lowercase field names in responses. Input matching is case-insensitive either way.
