package ezpi

import (
	"fmt"
	"net/mail"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Validation rules, used in `ez:"..."` struct tags:
//
//	required    must not be empty / zero
//	email       must be a valid email address
//	url         must be an http(s) URL
//	min=N       text: at least N characters; number: at least N; list: at least N items
//	max=N       text: at most N characters;  number: at most N;  list: at most N items
//	oneof=a|b   must be one of the listed values

// checkTags makes sure every ez tag is valid, so typos fail at startup.
func checkTags(t reflect.Type) error {
	return checkTagsSeen(t, map[reflect.Type]bool{})
}

func checkTagsSeen(t reflect.Type, seen map[reflect.Type]bool) error {
	if seen[t] {
		return nil
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		for _, rule := range rules(sf) {
			name, val, _ := strings.Cut(rule, "=")
			switch name {
			case "required", "email", "url":
			case "min", "max":
				if _, err := strconv.ParseFloat(val, 64); err != nil {
					return fmt.Errorf("field %s: %s needs a number, like %s=3", sf.Name, name, name)
				}
			case "oneof":
				if val == "" {
					return fmt.Errorf("field %s: oneof needs values, like oneof=red|blue", sf.Name)
				}
			default:
				return fmt.Errorf("field %s: unknown ez rule %q", sf.Name, name)
			}
		}
		if inner := elemStruct(sf.Type); inner != nil {
			if err := checkTagsSeen(inner, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func rules(sf reflect.StructField) []string {
	tag := sf.Tag.Get("ez")
	if tag == "" {
		return nil
	}
	return strings.Split(tag, ",")
}

// elemStruct returns the struct type inside T, *T, []T or []*T, if any.
func elemStruct(t reflect.Type) reflect.Type {
	t = derefType(t)
	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = derefType(t.Elem())
	}
	if t.Kind() == reflect.Struct {
		return t
	}
	return nil
}

// validate returns a message per invalid field, keyed by JSON name.
func validate(v reflect.Value) map[string]string {
	out := map[string]string{}
	validateInto(v, "", out)
	return out
}

func validateInto(v reflect.Value, prefix string, out map[string]string) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name := prefix + fieldName(sf)
		fv := v.Field(i)
		if msg := check(fv, rules(sf)); msg != "" {
			out[name] = msg
			continue
		}

		// Look inside nested structs and lists of structs.
		inner := fv
		if inner.Kind() == reflect.Pointer {
			if inner.IsNil() {
				continue
			}
			inner = inner.Elem()
		}
		switch inner.Kind() {
		case reflect.Struct:
			validateInto(inner, name+".", out)
		case reflect.Slice, reflect.Array:
			if elemStruct(inner.Type()) == nil {
				continue
			}
			for j := 0; j < inner.Len(); j++ {
				el := inner.Index(j)
				if el.Kind() == reflect.Pointer {
					if el.IsNil() {
						continue
					}
					el = el.Elem()
				}
				validateInto(el, fmt.Sprintf("%s[%d].", name, j), out)
			}
		}
	}
}

// check runs one field's rules and returns the first failure message.
func check(v reflect.Value, rules []string) string {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			for _, r := range rules {
				if r == "required" {
					return "is required"
				}
			}
			return ""
		}
		v = v.Elem()
	}

	if isEmpty(v) {
		for _, r := range rules {
			if r == "required" {
				return "is required"
			}
		}
		return "" // optional and empty: skip the other rules
	}

	for _, rule := range rules {
		name, val, _ := strings.Cut(rule, "=")
		switch name {
		case "email":
			s := v.String()
			addr, err := mail.ParseAddress(s)
			if err != nil || addr.Address != s || !strings.Contains(s[strings.LastIndex(s, "@"):], ".") {
				return "must be a valid email"
			}
		case "url":
			u, err := url.Parse(v.String())
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return "must be a valid URL"
			}
		case "min", "max":
			limit, _ := strconv.ParseFloat(val, 64)
			n, unit := measure(v)
			if name == "min" && n < limit {
				return fmt.Sprintf("must be at least %s%s", val, unit)
			}
			if name == "max" && n > limit {
				return fmt.Sprintf("must be at most %s%s", val, unit)
			}
		case "oneof":
			opts := strings.Split(val, "|")
			got := fmt.Sprint(v.Interface())
			ok := false
			for _, o := range opts {
				if o == got {
					ok = true
					break
				}
			}
			if !ok {
				return "must be one of: " + strings.Join(opts, ", ")
			}
		}
	}
	return ""
}

func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return strings.TrimSpace(v.String()) == ""
	case reflect.Slice, reflect.Map:
		return v.Len() == 0
	default:
		return v.IsZero()
	}
}

// measure returns the size used by min/max, plus a unit for the message.
func measure(v reflect.Value) (float64, string) {
	switch v.Kind() {
	case reflect.String:
		return float64(utf8.RuneCountInString(v.String())), " characters"
	case reflect.Slice, reflect.Array, reflect.Map:
		return float64(v.Len()), " items"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), ""
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint()), ""
	case reflect.Float32, reflect.Float64:
		return v.Float(), ""
	}
	return 0, ""
}
