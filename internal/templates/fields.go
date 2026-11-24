// Package templates holds the settings servers share, versioned.
package templates

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Field keys. They double as the names shown in the UI, so they follow the
// shape of config.json rather than Go naming.
const (
	FieldPort    = "inbound.port"
	FieldPath    = "xhttp.path"
	FieldMode    = "xhttp.mode"
	FieldPadding = "xhttp.xPaddingBytes"
	FieldHost    = "xhttp.host"
)

// Field describes one setting a template carries.
type Field struct {
	Key string `json:"key"`
	// Type tells the UI what control to draw: enum | string | range | number.
	Type    string   `json:"type"`
	Options []string `json:"options,omitempty"`
	Default string   `json:"default"`
	// Fixed fields are shown but cannot be changed yet.
	Fixed bool `json:"fixed"`

	validate func(string) error
}

// FieldError is a value a field does not accept.
type FieldError struct {
	Key string
	// Code picks the explanation: invalid_path | invalid_mode | invalid_range | invalid_host | fixed | unknown_field.
	Code string
}

func (e *FieldError) Error() string { return fmt.Sprintf("%s: %s", e.Key, e.Code) }

var (
	pathPattern  = regexp.MustCompile(`^/[A-Za-z0-9/_.~-]{0,99}$`)
	rangePattern = regexp.MustCompile(`^(\d{1,5})-(\d{1,5})$`)
	hostPattern  = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
)

// Modes are the XHTTP modes Xray knows.
var Modes = []string{"auto", "packet-up", "stream-up", "stream-one"}

// Fields is every setting a template has, in display order.
var Fields = []Field{
	{
		// Changing the port also means changing the firewall and what the
		// outside check looks at; not built yet, so it is shown and fixed.
		Key: FieldPort, Type: "number", Default: "443", Fixed: true,
		validate: func(v string) error {
			if v != "443" {
				return &FieldError{Key: FieldPort, Code: "fixed"}
			}
			return nil
		},
	},
	{
		Key: FieldPath, Type: "string", Default: "/api/v2/stream",
		validate: func(v string) error {
			if !pathPattern.MatchString(v) {
				return &FieldError{Key: FieldPath, Code: "invalid_path"}
			}
			return nil
		},
	},
	{
		Key: FieldMode, Type: "enum", Options: Modes, Default: "auto",
		validate: func(v string) error {
			if !slices.Contains(Modes, v) {
				return &FieldError{Key: FieldMode, Code: "invalid_mode"}
			}
			return nil
		},
	},
	{
		Key: FieldPadding, Type: "range", Default: "100-1000",
		validate: func(v string) error {
			match := rangePattern.FindStringSubmatch(v)
			if match == nil {
				return &FieldError{Key: FieldPadding, Code: "invalid_range"}
			}
			low, _ := strconv.Atoi(match[1])
			high, _ := strconv.Atoi(match[2])
			if low > high || high > 10000 || high == 0 {
				return &FieldError{Key: FieldPadding, Code: "invalid_range"}
			}
			return nil
		},
	},
	{
		// Empty means "do not set": Xray then accepts any Host.
		Key: FieldHost, Type: "string", Default: "",
		validate: func(v string) error {
			if v != "" && (len(v) > 253 || !hostPattern.MatchString(v)) {
				return &FieldError{Key: FieldHost, Code: "invalid_host"}
			}
			return nil
		},
	},
}

// DefaultOverridable is what a new template lets servers change.
var DefaultOverridable = []string{FieldMode, FieldPadding}

func field(key string) *Field {
	for i := range Fields {
		if Fields[i].Key == key {
			return &Fields[i]
		}
	}
	return nil
}

// Normalize trims a value the way a person would expect before it is checked.
func Normalize(key, value string) string {
	value = strings.TrimSpace(value)
	if key == FieldHost {
		value = strings.ToLower(value)
	}
	return value
}

// ValidateValue checks one value for one field.
func ValidateValue(key, value string) error {
	f := field(key)
	if f == nil {
		return &FieldError{Key: key, Code: "unknown_field"}
	}
	return f.validate(value)
}

// Defaults is a complete set of values.
func Defaults() map[string]string {
	values := make(map[string]string, len(Fields))
	for _, f := range Fields {
		values[f.Key] = f.Default
	}
	return values
}

// complete validates values and fills in anything missing with the default, so
// a version always carries every field — including ones added after it was
// written.
func complete(values map[string]string) (map[string]string, error) {
	out := Defaults()
	for key, value := range values {
		value = Normalize(key, value)
		if err := ValidateValue(key, value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, nil
}

func checkOverridable(keys []string) ([]string, error) {
	out := []string{}
	for _, f := range Fields {
		if !slices.Contains(keys, f.Key) {
			continue
		}
		if f.Fixed {
			return nil, &FieldError{Key: f.Key, Code: "fixed"}
		}
		out = append(out, f.Key)
	}
	for _, key := range keys {
		if field(key) == nil {
			return nil, &FieldError{Key: key, Code: "unknown_field"}
		}
	}
	return out, nil
}

// IsFieldError reports whether err is a rejected value and returns it.
func IsFieldError(err error) (*FieldError, bool) {
	var fieldErr *FieldError
	ok := errors.As(err, &fieldErr)
	return fieldErr, ok
}
