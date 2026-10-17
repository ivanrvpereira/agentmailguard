package config

import (
	"context"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Screening is the [screening] table. Provider is jev or fake. Endpoint is the HTTPS URL of the jev engine and
// Model is the model name that it asks; empty means the built-in default. The fake engine ignores both.
type Screening struct{ Provider, Endpoint, Model string }

// ScreeningError names the invalid [screening] key path. It never contains the configured value.
type ScreeningError struct{ Path string }

func (e ScreeningError) Error() string { return "invalid screening: " + e.Path }

// LoadScreening reads the optional [screening] table. An absent table or key gives the default.
func LoadScreening(ctx context.Context, path string) (Screening, error) {
	d, _, err := document(ctx, path)
	if err != nil {
		return Screening{}, err
	}
	result := Screening{Provider: "jev"}
	value, present := d["screening"]
	if !present {
		return result, nil
	}
	table, ok := value.(map[string]any)
	if !ok {
		return Screening{}, ScreeningError{keyPath("screening")}
	}
	for _, key := range slices.Sorted(maps.Keys(table)) {
		fail := ScreeningError{keyPath("screening", key)}
		text, ok := table[key].(string)
		switch {
		case !ok:
			return Screening{}, fail
		case key == "provider" && (text == "jev" || text == "fake"):
			result.Provider = text
		case key == "endpoint" && validEndpoint(text):
			result.Endpoint = text
		case key == "model" && validModel.MatchString(text):
			result.Model = text
		default:
			return Screening{}, fail
		}
	}
	return result, nil
}

// validModel accepts model names such as typesafe/jev-1.13, jev-1.13.0, and jev-latest.
var validModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// validEndpoint accepts only an absolute HTTPS URL with a host name, no user info, and no fragment: the
// request carries mail text and the screening key. A port, if present, is a number from 1 to 65535.
func validEndpoint(text string) bool {
	u, err := url.Parse(text)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	// The host is the host name (in brackets if it is an IPv6 address), then optionally a colon and the port.
	name := u.Hostname()
	if strings.Contains(name, ":") {
		name = "[" + name + "]"
	}
	if u.Host == name {
		return true
	}
	port, ok := strings.CutPrefix(u.Host, name+":")
	n, err := strconv.ParseUint(port, 10, 16)
	return ok && err == nil && n > 0
}
