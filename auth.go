package ezpi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// User is the logged-in user on app.Private routes.
// Add it as a handler parameter: func(u ezpi.User) { ... }
type User struct {
	ID     string
	Claims map[string]any
}

type userKey struct{}

var b64 = base64.RawURLEncoding

// jwtHeader is the only header ezpi issues or accepts. Pinning the algorithm
// blocks "alg: none" and algorithm-swap attacks.
var jwtHeader = b64.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// Token creates a signed token (a JWT) for a user. Send it back to the client
// at login; the client sends it as "Authorization: Bearer <token>".
func (a *App) Token(userID string) (string, error) {
	if len(a.secret) < 32 {
		return "", errors.New("ezpi: EZPI_SECRET must be at least 32 characters")
	}
	if userID == "" {
		return "", errors.New("ezpi: Token needs a user ID")
	}
	now := time.Now()
	claims, err := json.Marshal(map[string]any{
		"sub": userID,
		"iat": now.Unix(),
		"exp": now.Add(a.tokenTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	unsigned := jwtHeader + "." + b64.EncodeToString(claims)
	return unsigned + "." + a.sign(unsigned), nil
}

func (a *App) sign(s string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(s))
	return b64.EncodeToString(mac.Sum(nil))
}

// authenticate checks the Bearer token. Every failure gives the same
// 401 so attackers learn nothing about why.
func (a *App) authenticate(r *http.Request) (User, error) {
	denied := Unauthorized("unauthorized")
	if len(a.secret) < 32 {
		a.log.Error("ezpi: private route hit but EZPI_SECRET is missing or shorter than 32 characters")
		return User{}, denied
	}

	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return User{}, denied
	}
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != jwtHeader {
		return User{}, denied
	}
	want := a.sign(parts[0] + "." + parts[1])
	if !hmac.Equal([]byte(parts[2]), []byte(want)) { // constant-time compare
		return User{}, denied
	}

	raw, err := b64.DecodeString(parts[1])
	if err != nil {
		return User{}, denied
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return User{}, denied
	}
	sub, _ := claims["sub"].(string)
	exp, _ := claims["exp"].(float64)
	if sub == "" || exp == 0 || time.Now().Unix() >= int64(exp) {
		return User{}, denied
	}
	return User{ID: sub, Claims: claims}, nil
}
