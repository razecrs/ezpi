package ezpi

import (
	"errors"
	"net/http"
)

// Err is an error that is safe to show to the client.
// Create one with the helpers below (NotFound, BadRequest, ...).
// Any other error returned from a handler is hidden from the client and logged.
type Err struct {
	Status  int               `json:"-"`
	Message string            `json:"error"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func (e *Err) Error() string { return e.Message }

// Error creates a client-visible error with any status code.
func Error(status int, message string) error {
	return &Err{Status: status, Message: message}
}

func BadRequest(message string) error   { return Error(http.StatusBadRequest, message) }
func Unauthorized(message string) error { return Error(http.StatusUnauthorized, message) }
func Forbidden(message string) error    { return Error(http.StatusForbidden, message) }
func NotFound(message string) error     { return Error(http.StatusNotFound, message) }
func Conflict(message string) error     { return Error(http.StatusConflict, message) }

// asErr finds an *Err anywhere in err's chain (so wrapped errors still work).
func asErr(err error) (*Err, bool) {
	var e *Err
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
