package config

import (
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// screening reads the optional [screening] table. An absent table or key gives the default.
func screening(d map[string]any) (Screening, error) {
	result := Screening{Provider: "jev"}
	value, present := d["screening"]
	if !present {
		return result, nil
	}
	table, ok := value.(map[string]any)
	if !ok {
		return Screening{}, fail("must be a table", "screening")
	}
	for _, key := range slices.Sorted(maps.Keys(table)) {
		text, ok := table[key].(string)
		switch {
		case key != "provider" && key != "endpoint" && key != "model":
			return Screening{}, fail("unknown key", "screening", key)
		case key == "provider" && ok && (text == "jev" || text == "fake"):
			result.Provider = text
		case key == "provider":
			return Screening{}, fail("must be jev or fake", "screening", key)
		case key == "endpoint" && ok && validEndpoint(text):
			result.Endpoint = text
		case key == "endpoint":
			return Screening{}, fail("must be an https URL", "screening", key)
		case key == "model" && ok && validModel.MatchString(text):
			result.Model = text
		default:
			return Screening{}, fail("must be a model name", "screening", key)
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
