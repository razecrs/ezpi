package ezpi

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Route is a registered route. Use it to add docs:
//
//	app.Get("/todos", listTodos).Doc("List all todos")
type Route struct {
	method  string
	path    string // OpenAPI form: /users/{id}
	private bool
	params  []pathParam
	input   reflect.Type
	inQuery bool
	output  reflect.Type
	status  int
	summary string
	tags    []string
}

type pathParam struct {
	name string
	typ  reflect.Type
}

// Doc sets the route's summary shown in the API docs.
func (rt *Route) Doc(summary string) *Route {
	rt.summary = summary
	return rt
}

// Tags groups the route under these names in the API docs.
// By default it's grouped by the first part of its path.
func (rt *Route) Tags(tags ...string) *Route {
	rt.tags = tags
	return rt
}

// Docs serves interactive API docs at path (e.g. "/docs") and the
// OpenAPI spec at path + "/openapi.json". Both are generated from your
// handlers' types and ez tags, so they never go out of date.
//
// Docs are off by default: they describe your whole API, so decide
// whether you want them public in production.
func (a *App) Docs(path string) {
	path = "/" + strings.Trim(path, "/")
	specPath := strings.TrimSuffix(path, "/") + "/openapi.json"

	a.mux.HandleFunc("GET "+specPath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.OpenAPI())
	})

	page := strings.ReplaceAll(docsHTML, "{{SPEC}}", specPath)
	pageCSP := "default-src 'none'; frame-ancestors 'none'; connect-src 'self'; img-src 'self' data:; " +
		"style-src " + docsCDN + " 'unsafe-inline'; script-src " + docsCDN + " '" + docsScriptHash(page) + "'"
	pagePattern, _ := convertPath(path)
	a.mux.HandleFunc("GET "+pagePattern, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", pageCSP)
		_, _ = w.Write([]byte(page))
	})
}

const docsCDN = "https://cdn.jsdelivr.net"

const docsHTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>API docs</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css">
</head>
<body>
<div id="docs"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({ url: "{{SPEC}}", dom_id: "#docs", persistAuthorization: false });</script>
</body>
</html>`

// docsScriptHash returns the CSP hash of the page's inline script, so only
// that exact script may run.
func docsScriptHash(page string) string {
	start := strings.LastIndex(page, "<script>") + len("<script>")
	end := strings.LastIndex(page, "</script>")
	sum := sha256.Sum256([]byte(page[start:end]))
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

// OpenAPI returns the OpenAPI 3.1 spec for every registered route.
func (a *App) OpenAPI() map[string]any {
	g := &schemaGen{names: map[reflect.Type]string{}, schemas: map[string]any{}}
	g.schemas["Error"] = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"error":      map[string]any{"type": "string"},
			"fields":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"request_id": map[string]any{"type": "string"},
		},
		"required": []string{"error"},
	}

	paths := map[string]any{}
	for _, rt := range a.routes {
		item, _ := paths[rt.path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[rt.path] = item
		}
		item[strings.ToLower(rt.method)] = g.operation(rt)
	}

	return map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "API", "version": "1.0.0"},
		"paths":   paths,
		"components": map[string]any{
			"schemas": g.schemas,
			"securitySchemes": map[string]any{
				"bearer": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "JWT"},
			},
		},
	}
}

func errorResponse(desc string) map[string]any {
	return map[string]any{
		"description": desc,
		"content": map[string]any{
			"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}},
		},
	}
}

func (g *schemaGen) operation(rt *Route) map[string]any {
	op := map[string]any{}
	if rt.summary != "" {
		op["summary"] = rt.summary
	}
	tags := rt.tags
	if len(tags) == 0 {
		if first := strings.Split(strings.Trim(rt.path, "/"), "/")[0]; first != "" && !strings.HasPrefix(first, "{") {
			tags = []string{first}
		}
	}
	if len(tags) > 0 {
		op["tags"] = tags
	}

	var params []any
	for _, p := range rt.params {
		params = append(params, map[string]any{
			"name": p.name, "in": "path", "required": true, "schema": g.schema(p.typ),
		})
	}
	if rt.input != nil && rt.inQuery {
		for i := 0; i < rt.input.NumField(); i++ {
			sf := rt.input.Field(i)
			if !sf.IsExported() || !isScalar(sf.Type) {
				continue
			}
			s := g.schema(sf.Type)
			applyRules(s, sf)
			params = append(params, map[string]any{
				"name": fieldName(sf), "in": "query", "required": hasRule(sf, "required"), "schema": s,
			})
		}
	}
	if len(params) > 0 {
		op["parameters"] = params
	}
	if rt.input != nil && !rt.inQuery {
		op["requestBody"] = map[string]any{
			"required": true,
			"content":  map[string]any{"application/json": map[string]any{"schema": g.schema(rt.input)}},
		}
	}

	ok := map[string]any{"description": http.StatusText(rt.status)}
	if rt.output != nil && rt.status != http.StatusNoContent {
		ok["content"] = map[string]any{"application/json": map[string]any{"schema": g.schema(rt.output)}}
	}
	responses := map[string]any{strconv.Itoa(rt.status): ok}
	if rt.input != nil || len(rt.params) > 0 {
		responses["400"] = errorResponse("Invalid input")
	}
	if rt.private {
		responses["401"] = errorResponse("Missing or invalid token")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
	}
	responses["default"] = errorResponse("Error")
	op["responses"] = responses
	return op
}

// schemaGen turns Go types into JSON Schemas. Named structs go into
// components/schemas and are referenced by $ref, which also handles recursion.
type schemaGen struct {
	names   map[reflect.Type]string
	schemas map[string]any
}

var timeType = reflect.TypeOf(time.Time{})

func (g *schemaGen) schema(t reflect.Type) map[string]any {
	if t == nil {
		return map[string]any{}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "format": "byte"} // []byte is base64 in JSON
		}
		return map[string]any{"type": "array", "items": g.schema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": g.schema(t.Elem())}
	case reflect.Struct:
		if t.Name() == "" {
			return g.structSchema(t)
		}
		name, ok := g.names[t]
		if !ok {
			name = g.uniqueName(t.Name())
			g.names[t] = name
			g.schemas[name] = map[string]any{} // placeholder, so recursion stops here
			g.schemas[name] = g.structSchema(t)
		}
		return map[string]any{"$ref": "#/components/schemas/" + name}
	}
	return map[string]any{} // interfaces etc.: anything goes
}

func (g *schemaGen) uniqueName(base string) string {
	name := base
	for i := 2; ; i++ {
		if _, taken := g.schemas[name]; !taken {
			return name
		}
		name = base + strconv.Itoa(i)
	}
}

func (g *schemaGen) structSchema(t reflect.Type) map[string]any {
	props := map[string]any{}
	var required []string
	g.addFields(t, props, &required)
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func (g *schemaGen) addFields(t reflect.Type, props map[string]any, required *[]string) {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		tag := strings.Split(sf.Tag.Get("json"), ",")[0]
		if tag == "-" {
			continue
		}
		// Embedded structs without a json name are flattened, like encoding/json does.
		if sf.Anonymous && tag == "" && derefType(sf.Type).Kind() == reflect.Struct {
			g.addFields(derefType(sf.Type), props, required)
			continue
		}
		if !sf.IsExported() {
			continue
		}
		name := tag
		if name == "" {
			name = sf.Name // what encoding/json writes
		}
		s := g.schema(sf.Type)
		applyRules(s, sf)
		props[name] = s
		if hasRule(sf, "required") {
			*required = append(*required, name)
		}
	}
}

func hasRule(sf reflect.StructField, rule string) bool {
	for _, r := range rules(sf) {
		if r == rule {
			return true
		}
	}
	return false
}

// applyRules adds ez validation rules to a field's schema.
func applyRules(s map[string]any, sf reflect.StructField) {
	if _, isRef := s["$ref"]; isRef {
		return
	}
	kind := derefType(sf.Type).Kind()
	for _, r := range rules(sf) {
		name, val, _ := strings.Cut(r, "=")
		n, _ := strconv.ParseFloat(val, 64)
		switch name {
		case "email":
			s["format"] = "email"
		case "url":
			s["format"] = "uri"
		case "min", "max":
			key := map[bool]string{true: "minimum", false: "maximum"}[name == "min"]
			switch kind {
			case reflect.String:
				key = map[bool]string{true: "minLength", false: "maxLength"}[name == "min"]
			case reflect.Slice, reflect.Array:
				key = map[bool]string{true: "minItems", false: "maxItems"}[name == "min"]
			case reflect.Map:
				key = map[bool]string{true: "minProperties", false: "maxProperties"}[name == "min"]
			}
			s[key] = n
		case "oneof":
			var enum []any
			for _, o := range strings.Split(val, "|") {
				if kind == reflect.String {
					enum = append(enum, o)
				} else if f, err := strconv.ParseFloat(o, 64); err == nil {
					enum = append(enum, f)
				}
			}
			s["enum"] = enum
		}
	}
}
