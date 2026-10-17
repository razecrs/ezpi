package ezpi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

var (
	ctxType     = reflect.TypeOf((*Ctx)(nil))
	contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
	userType    = reflect.TypeOf(User{})
	errorType   = reflect.TypeOf((*error)(nil)).Elem()
)

type argKind int

const (
	argCtx argKind = iota
	argContext
	argUser
	argInput // struct filled from the JSON body (or query string for GET/DELETE)
	argPath  // string/number filled from the URL path
)

type arg struct {
	kind  argKind
	typ   reflect.Type
	param string // path param name, for argPath
}

// build checks the handler's signature once, at startup, and returns an
// http.Handler that calls it. Bad signatures fail here instead of at request time.
func (a *App) build(rt *Route, params []string, handler any) (http.Handler, error) {
	method, private := rt.method, rt.private
	rt.status = http.StatusOK
	if method == http.MethodPost {
		rt.status = http.StatusCreated
	}

	fn := reflect.ValueOf(handler)
	if fn.Kind() != reflect.Func {
		// A plain value: app.Get("/health", "ok")
		rt.output = reflect.TypeOf(handler)
		return a.wrap(private, func(c *Ctx) {
			a.respond(c, http.StatusOK, handler)
		}), nil
	}

	t := fn.Type()
	args := make([]arg, t.NumIn())
	pathIdx, hasInput := 0, false
	for i := range args {
		in := t.In(i)
		switch {
		case in == ctxType:
			args[i] = arg{kind: argCtx}
		case in == contextType:
			args[i] = arg{kind: argContext}
		case in == userType:
			if !private {
				return nil, errors.New("ezpi.User only works on app.Private routes")
			}
			args[i] = arg{kind: argUser}
		case isStruct(in):
			if hasInput {
				return nil, errors.New("handler can only take one struct input")
			}
			if err := checkTags(derefType(in)); err != nil {
				return nil, err
			}
			hasInput = true
			args[i] = arg{kind: argInput, typ: in}
			rt.input = derefType(in)
			rt.inQuery = method == http.MethodGet || method == http.MethodDelete
		case isScalar(in):
			if pathIdx >= len(params) {
				return nil, fmt.Errorf("argument %d (%s) has no matching :param in the path", i+1, in)
			}
			args[i] = arg{kind: argPath, typ: in, param: params[pathIdx]}
			rt.params = append(rt.params, pathParam{name: params[pathIdx], typ: in})
			pathIdx++
		default:
			return nil, fmt.Errorf("unsupported argument type %s", in)
		}
	}
	if pathIdx != len(params) {
		return nil, fmt.Errorf("path has %d :params but the handler takes %d", len(params), pathIdx)
	}

	switch {
	case t.NumOut() > 2,
		t.NumOut() == 2 && t.Out(1) != errorType,
		t.NumOut() == 2 && t.Out(0) == errorType:
		return nil, errors.New("handler must return nothing, a value, an error, or (value, error)")
	}
	if t.NumOut() > 0 && t.Out(0) != errorType {
		rt.output = t.Out(0)
	} else {
		rt.status = http.StatusNoContent
	}
	success := rt.status

	return a.wrap(private, func(c *Ctx) {
		in := make([]reflect.Value, len(args))
		for i, ar := range args {
			v, err := a.fill(c, method, ar)
			if err != nil {
				a.fail(c, err)
				return
			}
			in[i] = v
		}

		out := fn.Call(in)
		if c.written {
			return
		}

		var result any
		for _, o := range out {
			if o.Type() == errorType {
				if !o.IsNil() {
					a.fail(c, o.Interface().(error))
					return
				}
				continue
			}
			result = o.Interface()
		}

		status := success
		if c.status != 0 {
			status = c.status
		}
		a.respond(c, status, result)
	}), nil
}

// wrap creates the Ctx and runs auth for private routes.
func (a *App) wrap(private bool, run func(c *Ctx)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := &Ctx{Request: r, Writer: w, RequestID: requestID(r)}
		if private {
			user, err := a.authenticate(r)
			if err != nil {
				a.fail(c, err)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), userKey{}, user))
			c.Request = r
			w.Header().Set("Cache-Control", "no-store")
		}
		run(c)
	})
}

func (a *App) fill(c *Ctx, method string, ar arg) (reflect.Value, error) {
	switch ar.kind {
	case argCtx:
		return reflect.ValueOf(c), nil
	case argContext:
		return reflect.ValueOf(c.Request.Context()), nil
	case argUser:
		return reflect.ValueOf(c.Request.Context().Value(userKey{}).(User)), nil
	case argPath:
		v := reflect.New(ar.typ).Elem()
		if err := setScalar(v, c.Request.PathValue(ar.param)); err != nil {
			return v, BadRequest(fmt.Sprintf("%s must be %s", ar.param, describe(ar.typ)))
		}
		return v, nil
	default: // argInput
		ptr := reflect.New(derefType(ar.typ))
		var err error
		if method == http.MethodGet || method == http.MethodDelete {
			err = bindQuery(ptr.Elem(), c.Request.URL.Query())
		} else {
			err = decodeBody(c.Request, ptr.Interface())
		}
		if err != nil {
			return ptr, err
		}
		if fields := validate(ptr.Elem()); len(fields) > 0 {
			return ptr, &Err{Status: http.StatusBadRequest, Message: "validation failed", Fields: fields}
		}
		if ar.typ.Kind() == reflect.Pointer {
			return ptr, nil
		}
		return ptr.Elem(), nil
	}
}

// decodeBody reads strict JSON: right content type, no unknown fields, no trailing junk.
func decodeBody(r *http.Request, dst any) error {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return Error(http.StatusRequestEntityTooLarge, "request too large")
		}
		return BadRequest("could not read request body")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil // empty body: let validation report missing fields
	}
	// Requiring JSON also blocks simple cross-site form posts (CSRF).
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "application/json" {
		return Error(http.StatusUnsupportedMediaType, "Content-Type must be application/json")
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &typeErr) && typeErr.Field != "":
			return BadRequest(fmt.Sprintf("%s has the wrong type", typeErr.Field))
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			return BadRequest(strings.TrimPrefix(err.Error(), "json: "))
		default:
			return BadRequest("invalid JSON")
		}
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return BadRequest("invalid JSON: body must contain a single object")
	}
	return nil
}

// bindQuery fills simple struct fields from ?key=value pairs.
func bindQuery(v reflect.Value, q url.Values) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() || !isScalar(sf.Type) {
			continue
		}
		name := fieldName(sf)
		raw, ok := q[name]
		if !ok || len(raw) == 0 {
			continue
		}
		if err := setScalar(v.Field(i), raw[0]); err != nil {
			return BadRequest(fmt.Sprintf("%s must be %s", name, describe(sf.Type)))
		}
	}
	return nil
}

func setScalar(v reflect.Value, s string) error {
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetFloat(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		v.SetBool(b)
	}
	return nil
}

func describe(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Bool:
		return "true or false"
	case reflect.String:
		return "text"
	default:
		return "a number"
	}
}

func isScalar(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func isStruct(t reflect.Type) bool { return derefType(t).Kind() == reflect.Struct }

func derefType(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer {
		return t.Elem()
	}
	return t
}

// fieldName is the JSON name of a field: its json tag, or the name with a lowercase first letter.
func fieldName(sf reflect.StructField) string {
	if tag := strings.Split(sf.Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
		return tag
	}
	return strings.ToLower(sf.Name[:1]) + sf.Name[1:]
}

// respond writes v as JSON.
func (a *App) respond(c *Ctx, status int, v any) {
	c.written = true
	if status == http.StatusNoContent {
		c.Writer.WriteHeader(status)
		return
	}
	writeJSON(c.Writer, status, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail turns any error into a response. *Err is shown as-is;
// anything else is logged and hidden behind a generic 500.
func (a *App) fail(c *Ctx, err error) {
	c.written = true
	var body Err
	if e, ok := asErr(err); ok {
		body = *e
	} else {
		a.log.Error("handler error", "request_id", c.RequestID,
			"method", c.Request.Method, "path", c.Request.URL.Path, "err", err)
		body = Err{Status: http.StatusInternalServerError, Message: "something went wrong"}
		if a.dev {
			body.Message = err.Error()
		}
	}
	if body.Status == 0 {
		body.Status = http.StatusInternalServerError
	}
	if body.Status == http.StatusUnauthorized {
		c.Writer.Header().Set("WWW-Authenticate", "Bearer")
	}
	if a.onError != nil {
		writeJSON(c.Writer, body.Status, a.onError(body))
		return
	}
	resp := map[string]any{"error": body.Message}
	if len(body.Fields) > 0 {
		resp["fields"] = body.Fields
	}
	if body.Status >= 500 {
		resp["request_id"] = c.RequestID
	}
	writeJSON(c.Writer, body.Status, resp)
}
